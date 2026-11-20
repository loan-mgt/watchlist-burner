package match

import (
	"testing"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
)

func item(id, name string, year int, tmdb string) jellyfin.Item {
	y := year
	it := jellyfin.Item{ID: id, Name: name, ProductionYear: &y}
	if tmdb != "" {
		it.ProviderIds = map[string]string{"Tmdb": tmdb}
	}
	return it
}

func TestMatchByTitleAndYear(t *testing.T) {
	m := NewMatcher([]jellyfin.Item{item("1", "The Game", 1997, "2649")})

	if got := m.Match(letterboxd.Film{Title: "The Game", Year: 1997}); got == nil || got.ID != "1" {
		t.Fatalf("got %+v", got)
	}
	if got := m.MatchTmdb("2649"); got == nil || got.ID != "1" {
		t.Fatalf("tmdb got %+v", got)
	}
	if got := m.MatchTmdb(""); got != nil {
		t.Fatalf("empty tmdb matched %+v", got)
	}
}

func TestMatchToleratesOneYear(t *testing.T) {
	// Two same-title items so the unique-title fallback cannot help.
	m := NewMatcher([]jellyfin.Item{
		item("1", "Memories of Murder", 2004, ""),
		item("2", "Memories of Murder", 2030, ""),
	})

	if got := m.Match(letterboxd.Film{Title: "Memories of Murder", Year: 2003}); got == nil || got.ID != "1" {
		t.Fatalf("expected a -1 year match, got %+v", got)
	}
	if got := m.Match(letterboxd.Film{Title: "Memories of Murder", Year: 2005}); got == nil || got.ID != "1" {
		t.Fatalf("expected a +1 year match, got %+v", got)
	}
	if got := m.Match(letterboxd.Film{Title: "Memories of Murder", Year: 2020}); got != nil {
		t.Fatalf("a large gap must not match, got %+v", got)
	}
}

func TestTitleOnlyRequiresUniqueness(t *testing.T) {
	unique := NewMatcher([]jellyfin.Item{item("1", "U", 2006, "")})
	if got := unique.Match(letterboxd.Film{Title: "U", Year: 1999}); got == nil {
		t.Fatal("a unique title should match when the year is off")
	}

	ambiguous := NewMatcher([]jellyfin.Item{
		item("1", "Spider", 2002, ""),
		item("2", "Spider", 2024, ""),
	})
	if got := ambiguous.Match(letterboxd.Film{Title: "Spider", Year: 1990}); got != nil {
		t.Fatalf("ambiguous title must not match arbitrarily, got %+v", got)
	}
	if got := ambiguous.Match(letterboxd.Film{Title: "Spider", Year: 2002}); got == nil || got.ID != "1" {
		t.Fatalf("year must disambiguate, got %+v", got)
	}
}

func TestMatchUsesOriginalTitle(t *testing.T) {
	y := 2001
	it := jellyfin.Item{
		ID:             "1",
		Name:           "Amelie",
		OriginalTitle:  "Le Fabuleux Destin d'Amélie Poulain",
		ProductionYear: &y,
	}
	m := NewMatcher([]jellyfin.Item{it})

	if got := m.Match(letterboxd.Film{Title: "Le Fabuleux Destin d'Amelie Poulain", Year: 2001}); got == nil {
		t.Fatal("original title should be indexed")
	}
}

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"Amélie":                    "amelie",
		"The Game":                  "game",
		"A Quiet Place":             "quiet place",
		"Fast & Furious":            "fast and furious",
		"The Godfather Part II":     "godfather part 2",
		"Rocky IV":                  "rocky 4",
		"  Spider-Man: No Way Home": "spider man no way home",
		"Les Misérables":            "miserables",
		"2001: A Space Odyssey":     "2001 a space odyssey",
		"I, Robot":                  "i robot",
	}
	for in, want := range tests {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
