// Command mnsz-watch polls program pages on mnsz.jegy.hu (Miskolci Nemzeti
// Színház) and sends an ntfy push when tickets for a watched performance date
// go on sale.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	defaultBase  = "https://mnsz.jegy.hu"
	defaultNtfy  = "https://ntfy.sh"
	userAgent    = "Mozilla/5.0 (compatible; mnsz-watch/1.0)"
	maxBodyBytes = 5 << 20
	minPoll      = 5 * time.Second
	// failureAlertAfter consecutive failed checks of one target (fetch error, or
	// the date missing from the page) trigger one warning push, so a site
	// redesign cannot silently blind the watcher.
	failureAlertAfter = 30
)

var (
	slugRe  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	topicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

func main() {
	poll := flag.Duration("poll", 60*time.Second, "time between checks of each target")
	once := flag.Bool("once", false, "check every target once, print the result and exit without sending pushes")
	base := flag.String("base", defaultBase, "ticket site base URL")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mnsz-watch [-poll 60s] [-once] <program-slug>@<YYYY-MM-DD> ...\n\n"+
			"example: mnsz-watch banyavirag-176949@2026-12-15 sararany-192632@2027-01-19\n\n"+
			"env: NTFY_TOPIC (required unless -once), NTFY_URL (default %s)\n\n", defaultNtfy)
		flag.PrintDefaults()
	}
	flag.Parse()

	targets, err := parseTargets(flag.Args())
	if err == nil && *poll < minPoll {
		err = fmt.Errorf("-poll must be at least %s", minPoll)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		flag.Usage()
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 30 * time.Second}
	site := &site{base: strings.TrimRight(*base, "/"), client: client}

	if *once {
		os.Exit(checkOnce(ctx, site, targets))
	}

	// The topic is the only credential a public ntfy.sh topic has, so it is never logged.
	topic := os.Getenv("NTFY_TOPIC")
	if !topicRe.MatchString(topic) {
		logger.Error("NTFY_TOPIC must be set to 1-64 characters of A-Z, a-z, 0-9, _ or -")
		os.Exit(2)
	}
	server := os.Getenv("NTFY_URL")
	if server == "" {
		server = defaultNtfy
	}

	w := &watcher{
		site:    site,
		notify:  &notifier{server: server, topic: topic, client: client},
		log:     logger,
		targets: targets,
		poll:    *poll,
		now:     time.Now,
	}
	logger.Info("starting", "targets", fmt.Sprint(targets), "poll", poll.String(), "ntfy", server)
	if err := w.run(ctx); err != nil {
		logger.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func checkOnce(ctx context.Context, s checker, targets []target) int {
	code := 0
	for _, t := range targets {
		res, err := s.check(ctx, t)
		if err != nil {
			fmt.Printf("%s error: %v\n", t, err)
			code = 1
			continue
		}
		fmt.Printf("%s %s %s %s\n", t, res.Status, res.DateTime, res.URL)
	}
	return code
}

// target is one performance date of one program.
type target struct {
	Slug string // e.g. banyavirag-176949
	Date string // YYYY-MM-DD
}

func (t target) String() string { return t.Slug + "@" + t.Date }

// parseTarget accepts "<slug>@<date>" or "<program page URL>@<date>".
func parseTarget(s string) (target, error) {
	i := strings.LastIndex(s, "@")
	if i < 0 {
		return target{}, fmt.Errorf("target %q: want <program-slug>@<YYYY-MM-DD>", s)
	}
	ref, date := strings.TrimSuffix(s[:i], "/"), s[i+1:]
	if j := strings.LastIndex(ref, "/program/"); j >= 0 {
		ref = ref[j+len("/program/"):]
	}
	if !slugRe.MatchString(ref) {
		return target{}, fmt.Errorf("target %q: %q is not a program slug like banyavirag-176949", s, ref)
	}
	if d, err := time.Parse(time.DateOnly, date); err != nil || d.Format(time.DateOnly) != date {
		return target{}, fmt.Errorf("target %q: %q is not a YYYY-MM-DD date", s, date)
	}
	return target{Slug: ref, Date: date}, nil
}

func parseTargets(args []string) ([]target, error) {
	if len(args) == 0 {
		return nil, errors.New("no targets given")
	}
	seen := map[target]bool{}
	var out []target
	for _, a := range args {
		t, err := parseTarget(a)
		if err != nil {
			return nil, err
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

type checker interface {
	check(ctx context.Context, t target) (result, error)
}

type site struct {
	base   string
	client *http.Client
}

func (s *site) programURL(t target) string { return s.base + "/program/" + t.Slug }

func (s *site) check(ctx context.Context, t target) (result, error) {
	url := s.programURL(t)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return result{}, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Referer", s.base+"/")
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result{}, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return result{}, fmt.Errorf("GET %s: %w", url, err)
	}
	res := findPerformance(page, t.Date)
	if res.Status == available {
		switch {
		case res.URL == "":
			res.URL = url
		case strings.HasPrefix(res.URL, "/"):
			res.URL = s.base + res.URL
		}
	}
	return res, nil
}

type sender interface {
	send(ctx context.Context, m message) error
}

// message is an ntfy JSON publish request; JSON keeps the Hungarian text
// intact, which plain HTTP headers would not.
type message struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

type notifier struct {
	server string
	topic  string
	client *http.Client
}

func (n *notifier) send(ctx context.Context, m message) error {
	m.Topic = n.topic
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// JSON publishing goes to the server root, with the topic in the body.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(n.server, "/")+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ntfy publish: %s", resp.Status)
	}
	return nil
}

type state struct {
	target
	done     bool
	failures int
	warned   bool
}

type watcher struct {
	site    checker
	notify  sender
	log     *slog.Logger
	targets []target
	poll    time.Duration
	now     func() time.Time
}

// run polls until every target is notified or its date has passed, then idles
// until stopped. It does not exit on completion: under --restart unless-stopped
// an exit would restart it and repeat the push.
func (w *watcher) run(ctx context.Context) error {
	if err := w.notify.send(ctx, startupMessage(w.targets)); err != nil {
		return fmt.Errorf("startup push: %w", err)
	}
	states := make([]*state, len(w.targets))
	for i, t := range w.targets {
		states[i] = &state{target: t}
	}
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		pending := 0
		for _, st := range states {
			if st.done {
				continue
			}
			w.step(ctx, st)
			if !st.done {
				pending++
			}
		}
		if pending == 0 {
			w.log.Info("all targets finished, idling until stopped")
			<-ctx.Done()
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *watcher) step(ctx context.Context, st *state) {
	if st.Date < w.now().UTC().Format(time.DateOnly) {
		st.done = true
		w.log.Info("performance date passed, no longer watching", "target", st.target.String())
		return
	}
	res, err := w.site.check(ctx, st.target)
	if ctx.Err() != nil {
		return
	}
	switch {
	case err != nil:
		w.fail(ctx, st, err.Error())
	case res.Status == notFound:
		w.fail(ctx, st, "date not listed on the program page")
	case res.Status == unavailable:
		if st.failures > 0 {
			w.log.Info("checks recovered", "target", st.target.String(), "after_failures", st.failures)
		}
		st.failures, st.warned = 0, false
		w.log.Info("not on sale", "target", st.target.String(), "performance", res.DateTime)
	case res.Status == available:
		w.log.Info("TICKETS AVAILABLE", "target", st.target.String(), "performance", res.DateTime, "event_id", res.EventID, "url", res.URL)
		if err := w.notify.send(ctx, availableMessage(st.target, res)); err != nil {
			w.log.Error("push failed, retrying next poll", "target", st.target.String(), "err", err)
			return
		}
		st.done = true
		w.log.Info("push sent, target done", "target", st.target.String())
	}
}

func (w *watcher) fail(ctx context.Context, st *state, reason string) {
	st.failures++
	w.log.Warn("check failed", "target", st.target.String(), "consecutive", st.failures, "reason", reason)
	if st.failures < failureAlertAfter || st.warned {
		return
	}
	if err := w.notify.send(ctx, warningMessage(st.target, st.failures, reason)); err != nil {
		w.log.Error("warning push failed", "target", st.target.String(), "err", err)
		return
	}
	st.warned = true
}

// Push texts are user-facing, so they are Hungarian.

func startupMessage(targets []target) message {
	lines := make([]string, len(targets))
	for i, t := range targets {
		lines[i] = fmt.Sprintf("%s – %s", t.Slug, t.Date)
	}
	return message{
		Title:    "MNSZ figyelő elindult",
		Message:  "Figyelt előadások:\n" + strings.Join(lines, "\n"),
		Priority: 2,
		Tags:     []string{"eyes"},
	}
}

func availableMessage(t target, r result) message {
	return message{
		Title:    "MNSZ - Jegy elérhető!",
		Message:  fmt.Sprintf("Elérhető a jegy!\n\nElőadás: %s\nDátum: %s\nEvent ID: %s\n\n%s", t.Slug, r.DateTime, r.EventID, r.URL),
		Priority: 5,
		Tags:     []string{"ticket", "rotating_light"},
		Click:    r.URL,
	}
}

func warningMessage(t target, failures int, reason string) message {
	return message{
		Title: "MNSZ figyelő: nem látja az előadást",
		Message: fmt.Sprintf("%s – %s\n%d egymást követő sikertelen ellenőrzés.\nUtolsó hiba: %s\n\nLehet, hogy megváltozott az oldal vagy elmaradt az előadás.",
			t.Slug, t.Date, failures, reason),
		Priority: 4,
		Tags:     []string{"warning"},
		Click:    defaultBase + "/program/" + t.Slug,
	}
}
