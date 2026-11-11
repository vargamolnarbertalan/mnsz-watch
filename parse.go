package main

import (
	"bytes"
	"html"
	"regexp"
	"slices"
	"strings"
)

type status int

const (
	notFound status = iota
	unavailable
	available
)

func (s status) String() string {
	switch s {
	case available:
		return "available"
	case unavailable:
		return "unavailable"
	default:
		return "not-found"
	}
}

// result is what the program page says about one performance date.
type result struct {
	Status   status
	DateTime string
	EventID  string
	URL      string
}

// The page is server-rendered: one <div class="ticketChooser row"> per
// performance, holding a <time datetime="YYYY-MM-DD HH:MM:SS"> and one button:
//   - on sale:   <a href="..." class="button right moreIcon buyTicketButton">Jegyvásárlás</a>
//   - sold out:  <a class="button disabled right buyTicketButton">Elfogyott</a> (no href)
//   - not yet:   <a class="button disabled right">nem elérhető</a>
var (
	blockStartRe = regexp.MustCompile(`<div\s+class="ticketChooser\s+row\b`)
	timeRe       = regexp.MustCompile(`<time\b[^>]*\sdatetime="([^"]+)"`)
	anchorRe     = regexp.MustCompile(`<a\b[^>]*>`)
	classRe      = regexp.MustCompile(`\sclass="([^"]*)"`)
	hrefRe       = regexp.MustCompile(`\shref="([^"]+)"`)
	eventIDRe    = regexp.MustCompile(`\sdata-neteventid="([^"]+)"`)
	sectionEnd   = []byte("</section>")
)

// buyAnchor returns the enabled buy button with a link, or nil when the
// performance cannot be bought (not yet on sale, or sold out).
func buyAnchor(block []byte) []byte {
	for _, tag := range anchorRe.FindAll(block, -1) {
		c := classRe.FindSubmatch(tag)
		if c == nil || !hrefRe.Match(tag) {
			continue
		}
		classes := strings.Fields(string(c[1]))
		if slices.Contains(classes, "buyTicketButton") && !slices.Contains(classes, "disabled") {
			return tag
		}
	}
	return nil
}

// findPerformance reports the best status among the performances on date
// (YYYY-MM-DD); a day can have more than one performance.
func findPerformance(page []byte, date string) result {
	best := result{Status: notFound}
	for _, block := range ticketBlocks(page) {
		m := timeRe.FindSubmatch(block)
		if m == nil || !bytes.HasPrefix(m[1], []byte(date)) {
			continue
		}
		r := result{Status: unavailable, DateTime: string(m[1])}
		if tag := buyAnchor(block); tag != nil {
			r.Status = available
			if h := hrefRe.FindSubmatch(tag); h != nil {
				r.URL = html.UnescapeString(string(h[1]))
			}
			if e := eventIDRe.FindSubmatch(tag); e != nil {
				r.EventID = string(e[1])
			}
		}
		if r.Status > best.Status {
			best = r
		}
	}
	return best
}

// ticketBlocks splits the page into one chunk per performance row. Every chunk
// also ends at </section>, so markup after the list cannot leak into the last one.
func ticketBlocks(page []byte) [][]byte {
	starts := blockStartRe.FindAllIndex(page, -1)
	blocks := make([][]byte, 0, len(starts))
	for i, s := range starts {
		end := len(page)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		block := page[s[0]:end]
		if j := bytes.Index(block, sectionEnd); j >= 0 {
			block = block[:j]
		}
		blocks = append(blocks, block)
	}
	return blocks
}
