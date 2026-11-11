package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in      string
		want    target
		wantErr bool
	}{
		{in: "banyavirag-176949@2026-12-15", want: target{Slug: "banyavirag-176949", Date: "2026-12-15"}},
		{in: "https://mnsz.jegy.hu/program/sararany-192632@2027-01-19", want: target{Slug: "sararany-192632", Date: "2027-01-19"}},
		{in: "https://mnsz.jegy.hu/program/sararany-192632/@2027-01-19", want: target{Slug: "sararany-192632", Date: "2027-01-19"}},
		{in: "sararany-192632", wantErr: true},
		{in: "sararany-192632@2027-1-19", wantErr: true},
		{in: "sararany-192632@2027-02-30", wantErr: true},
		{in: "../admin@2027-01-19", wantErr: true},
		{in: "https://mnsz.jegy.hu/program/sararany-192632/1444884@2027-01-19", wantErr: true},
		{in: "@2027-01-19", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseTarget(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseTarget(%q) error = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("parseTarget(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseTargetsRejectsEmptyAndDeduplicates(t *testing.T) {
	if _, err := parseTargets(nil); err == nil {
		t.Fatal("want an error for no targets")
	}
	got, err := parseTargets([]string{"a-1@2026-12-15", "https://mnsz.jegy.hu/program/a-1@2026-12-15"})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v; want one target", got, err)
	}
}

func TestValidTopic(t *testing.T) {
	for _, ok := range []string{"mnsz-0123456789abcdef", "a_b"} {
		if !topicRe.MatchString(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "a/b", "a b", "../x", strings.Repeat("a", 65)} {
		if topicRe.MatchString(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestNotifierSendsJSONToServerRoot(t *testing.T) {
	var got message
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
	}))
	defer srv.Close()

	n := &notifier{server: srv.URL + "/", topic: "topic-1", client: srv.Client()}
	if err := n.send(context.Background(), message{Title: "Jegy elérhető!", Priority: 5, Click: "https://x"}); err != nil {
		t.Fatal(err)
	}
	if path != "/" {
		t.Errorf("posted to %q, want the server root", path)
	}
	if got.Topic != "topic-1" || got.Title != "Jegy elérhető!" || got.Priority != 5 || got.Click != "https://x" {
		t.Errorf("unexpected payload %+v", got)
	}
}

func TestNotifierReportsHTTPErrorsWithoutTopic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	n := &notifier{server: srv.URL, topic: "secret-topic", client: srv.Client()}
	err := n.send(context.Background(), message{Title: "x"})
	if err == nil || strings.Contains(err.Error(), "secret-topic") {
		t.Fatalf("want an error that does not leak the topic, got %v", err)
	}
}

// fakeSite returns scripted results per call.
type fakeSite struct {
	results []result
	errs    []error
	calls   int
}

func (f *fakeSite) check(ctx context.Context, t target) (result, error) {
	i := f.calls
	f.calls++
	if i >= len(f.results) {
		i = len(f.results) - 1
	}
	return f.results[i], f.errs[i]
}

type fakeNotifier struct{ sent []message }

func (f *fakeNotifier) send(ctx context.Context, m message) error {
	f.sent = append(f.sent, m)
	return nil
}

func newTestWatcher(s checker, n sender) *watcher {
	return &watcher{
		site:   s,
		notify: n,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:    func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) },
	}
}

func TestStepNotifiesOnceWhenAvailable(t *testing.T) {
	site := &fakeSite{results: []result{{Status: available, DateTime: "2026-12-15 19:00:00", URL: "https://buy"}}, errs: []error{nil}}
	n := &fakeNotifier{}
	w := newTestWatcher(site, n)
	st := &state{target: target{Slug: "a-1", Date: "2026-12-15"}}

	w.step(context.Background(), st)

	if !st.done || len(n.sent) != 1 || n.sent[0].Priority != 5 || n.sent[0].Click != "https://buy" {
		t.Fatalf("done=%v sent=%+v", st.done, n.sent)
	}
}

func TestStepWarnsOnceAfterRepeatedFailures(t *testing.T) {
	site := &fakeSite{results: []result{{Status: notFound}}, errs: []error{nil}}
	n := &fakeNotifier{}
	w := newTestWatcher(site, n)
	st := &state{target: target{Slug: "a-1", Date: "2026-12-15"}}

	for i := 0; i < failureAlertAfter*2; i++ {
		w.step(context.Background(), st)
	}

	if st.done || len(n.sent) != 1 || n.sent[0].Priority != 4 {
		t.Fatalf("done=%v sent=%+v; want exactly one warning", st.done, n.sent)
	}
}

func TestStepStopsAfterThePerformanceDate(t *testing.T) {
	site := &fakeSite{results: []result{{Status: unavailable}}, errs: []error{nil}}
	w := newTestWatcher(site, &fakeNotifier{})
	st := &state{target: target{Slug: "a-1", Date: "2026-11-11"}}

	w.step(context.Background(), st)

	if !st.done || site.calls != 0 {
		t.Fatalf("done=%v calls=%d; want done without fetching", st.done, site.calls)
	}
}
