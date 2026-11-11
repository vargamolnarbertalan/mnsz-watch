package main

import (
	"os"
	"testing"
)

// The fixture is the real program page fetched on 2026-11-12: the 2026-11-27 and
// 2026-11-28 performances were on sale, 2027-01-19 and 2027-01-20 were not.
func loadFixture(t *testing.T) []byte {
	t.Helper()
	page, err := os.ReadFile("testdata/sararany-192632.html")
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestFindPerformanceAvailable(t *testing.T) {
	got := findPerformance(loadFixture(t), "2026-11-27")
	want := result{
		Status:   available,
		DateTime: "2026-11-27 19:00:00",
		EventID:  "1444884",
		URL:      "https://mnsz.jegy.hu/program/sararany-192632/1444884",
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestFindPerformanceUnavailable(t *testing.T) {
	got := findPerformance(loadFixture(t), "2027-01-19")
	want := result{Status: unavailable, DateTime: "2027-01-19 19:00:00"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Sold-out performances keep the buyTicketButton class but add "disabled" and
// drop the href ("Elfogyott"). They must stay unavailable, so returned tickets
// are still caught. Fixture: the real page fetched on 2026-11-12, where
// 2026-11-26 and 2026-11-27 were sold out.
func TestFindPerformanceSoldOutIsUnavailable(t *testing.T) {
	page, err := os.ReadFile("testdata/muveszet-192641.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2026-11-26", "2026-11-27", "2027-01-19"} {
		if got := findPerformance(page, date); got.Status != unavailable {
			t.Errorf("%s: got %+v, want unavailable", date, got)
		}
	}
}

func TestFindPerformanceNeedsEnabledButtonWithHref(t *testing.T) {
	cases := map[string]status{
		`<a class="button disabled right buyTicketButton" data-eventlink="https://x/1">Elfogyott</a>`: unavailable,
		`<a class="button right buyTicketButton" data-neteventid="1">Jegyvásárlás</a>`:                unavailable,
		`<a class="buyTicketButtonX" href="https://x/1">x</a>`:                                        unavailable,
		`<a href="https://x/1" class="buyTicketButton">Jegyvásárlás</a>`:                              available,
	}
	for anchor, want := range cases {
		page := []byte(`<div class="ticketChooser row "><time datetime="2026-12-15 19:00:00"></time>` + anchor + `</div></section>`)
		if got := findPerformance(page, "2026-12-15"); got.Status != want {
			t.Errorf("%s: got %s, want %s", anchor, got.Status, want)
		}
	}
}

func TestFindPerformanceNotListed(t *testing.T) {
	for _, page := range [][]byte{loadFixture(t), nil, []byte("<html><body>maintenance</body></html>")} {
		if got := findPerformance(page, "2027-02-01"); got.Status != notFound {
			t.Fatalf("got %+v, want notFound", got)
		}
	}
}

// The last block must stop at </section>, so a buy button further down the page
// (another program's teaser) cannot mark the date as available.
func TestFindPerformanceLastBlockEndsAtSection(t *testing.T) {
	page := []byte(`<section>
<div class="ticketChooser row "><time class="ticketTime" datetime="2027-01-19 19:00:00"></time>
<a class="button disabled right">nem elérhető</a></div>
</section>
<a href="/program/other-1/2" class="button buyTicketButton" data-neteventid="2">Jegyvásárlás</a>`)
	if got := findPerformance(page, "2027-01-19"); got.Status != unavailable {
		t.Fatalf("got %+v, want unavailable", got)
	}
}

// Two performances on one day: one on sale is enough.
func TestFindPerformancePrefersAvailableOnSameDay(t *testing.T) {
	page := []byte(`<section>
<div class="ticketChooser row "><time datetime="2026-12-15 15:00:00"></time><a class="button disabled">nem elérhető</a></div>
<div class="ticketChooser row "><time datetime="2026-12-15 19:00:00"></time>
<a href="https://mnsz.jegy.hu/program/x-1/9?a=1&amp;b=2" class="button right buyTicketButton" data-neteventid="9">Jegyvásárlás</a></div>
</section>`)
	got := findPerformance(page, "2026-12-15")
	want := result{Status: available, DateTime: "2026-12-15 19:00:00", EventID: "9", URL: "https://mnsz.jegy.hu/program/x-1/9?a=1&b=2"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
