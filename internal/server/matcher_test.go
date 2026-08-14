package server

import (
	"testing"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
)

func yearPtr(y int) *int { return &y }

func item(id, name string, year int, played bool) jellyfin.Item {
	return jellyfin.Item{
		ID:             id,
		Name:           name,
		ProductionYear: yearPtr(year),
		UserData:       &jellyfin.UserData{Played: played},
	}
}

func film(title string, year int) letterboxd.Film {
	return letterboxd.Film{Slug: title, Title: title, Year: year, URL: "https://letterboxd.com/film/" + title + "/"}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"The Shawshank Redemption", "shawshankredemption"},
		{"A Clockwork Orange", "clockworkorange"},
		{"An American Werewolf in London", "americanwerewolfinlondon"},
		{"  In the Mood for Love  ", "inthemoodforlove"},
		{"Mad Max: Fury Road", "madmaxfuryroad"},
		{"Dr. Strangelove", "drstrangelove"},
		{"Pulp Fiction", "pulpfiction"},
	}
	for _, c := range cases {
		if got := normalize(c.in); got != c.want {
			t.Errorf("normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatcherMatchesByTitleAndYear(t *testing.T) {
	m := NewMatcher([]jellyfin.Item{
		item("1", "The Terminator", 1984, false),
		item("2", "Terminator 2: Judgment Day", 1991, false),
	})
	if got := m.Match(film("The Terminator", 1984)); got == nil || got.ID != "1" {
		t.Fatalf("expected match id 1, got %+v", got)
	}
	if got := m.Match(film("The Terminator", 1991)); got != nil {
		t.Fatalf("expected no match for wrong year, got %+v", got)
	}
}

func TestMatcherFallsBackToTitleWhenYearUnknown(t *testing.T) {
	m := NewMatcher([]jellyfin.Item{
		item("1", "Dune", 1984, false),
		item("2", "Alien", 1979, false),
	})
	if got := m.Match(film("Dune", 0)); got == nil || got.ID != "1" {
		t.Fatalf("expected title-only match for unknown year, got %+v", got)
	}
	if got := m.Match(film("Alien", 0)); got == nil || got.ID != "2" {
		t.Fatalf("expected title-only match for unknown year, got %+v", got)
	}
}

func TestMatcherPicksFirstDuplicate(t *testing.T) {
	m := NewMatcher([]jellyfin.Item{
		item("1", "Blade Runner", 1982, false),
		item("2", "Blade Runner", 1982, false),
	})
	if got := m.Match(film("Blade Runner", 1982)); got == nil || got.ID != "1" {
		t.Fatalf("expected first duplicate, got %+v", got)
	}
}

func TestMatcherIgnoresEmptyTitles(t *testing.T) {
	m := NewMatcher([]jellyfin.Item{
		item("1", "", 1984, false),
		item("2", "  ", 1984, false),
	})
	if got := m.Match(film("Dune", 1984)); got != nil {
		t.Fatalf("expected no match against empty-titled items, got %+v", got)
	}
}
