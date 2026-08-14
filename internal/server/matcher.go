package server

import (
	"strings"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
)

// Matcher matches Letterboxd films to Jellyfin items by normalized title +
// year, falling back to title alone when a film's year is unknown.
type Matcher struct {
	byKey   map[matchKey][]jellyfin.Item
	byTitle map[string][]jellyfin.Item
}

// matchKey is the identity used to join Letterboxd films with Jellyfin items:
// the normalized title plus the production year.
type matchKey struct {
	norm string
	year int
}

func NewMatcher(movies []jellyfin.Item) *Matcher {
	m := &Matcher{
		byKey:   make(map[matchKey][]jellyfin.Item),
		byTitle: make(map[string][]jellyfin.Item),
	}
	for _, it := range movies {
		norm := normalize(it.Name)
		if norm == "" {
			continue
		}
		k := matchKey{norm: norm, year: yearOf(it)}
		m.byKey[k] = append(m.byKey[k], it)
		m.byTitle[norm] = append(m.byTitle[norm], it)
	}
	return m
}

// Match returns the Jellyfin item for a Letterboxd film, if found. Films
// whose year failed to parse match by normalized title alone.
func (m *Matcher) Match(f letterboxd.Film) *jellyfin.Item {
	norm := normalize(f.Title)
	if norm == "" {
		return nil
	}
	if f.Year > 0 {
		if it := firstOf(m.byKey[matchKey{norm: norm, year: f.Year}]); it != nil {
			return it
		}
		return nil
	}
	return firstOf(m.byTitle[norm])
}

// firstOf returns the first item in the slice, or nil when empty. Ties are
// broken by Jellyfin's item order.
func firstOf(items []jellyfin.Item) *jellyfin.Item {
	if len(items) == 0 {
		return nil
	}
	it := items[0]
	return &it
}

func yearOf(it jellyfin.Item) int {
	if it.ProductionYear == nil {
		return 0
	}
	return *it.ProductionYear
}

// keyOf builds the match key for a title and year. The key's norm field is
// empty when the title normalizes to nothing.
func keyOf(name string, year int) matchKey {
	return matchKey{norm: normalize(name), year: year}
}

// normalize lowercases, strips leading articles and punctuation.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "the ")
	s = strings.TrimPrefix(s, "a ")
	s = strings.TrimPrefix(s, "an ")

	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r >= 0x80 {
			// keep accented letters, drop other unicode punctuation
			b.WriteRune(r)
		}
	}
	return b.String()
}
