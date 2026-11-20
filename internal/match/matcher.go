// Package match joins Letterboxd films to Jellyfin library items. It is
// deliberately small and pure: title+year first, then a one-year tolerance
// (production vs release year), then a unique-title fallback, then TMDB id when
// the caller managed to enrich the film.
package match

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
)

type key struct {
	title string
	year  int
}

// Matcher indexes a Jellyfin library for lookups.
type Matcher struct {
	byYear  map[key][]jellyfin.Item
	byTitle map[string][]jellyfin.Item
	byTmdb  map[string]jellyfin.Item
}

// NewMatcher indexes the given library items.
func NewMatcher(items []jellyfin.Item) *Matcher {
	m := &Matcher{
		byYear:  make(map[key][]jellyfin.Item, len(items)),
		byTitle: make(map[string][]jellyfin.Item, len(items)),
		byTmdb:  make(map[string]jellyfin.Item, len(items)),
	}
	for _, it := range items {
		if id := it.TmdbID(); id != "" {
			if _, exists := m.byTmdb[id]; !exists {
				m.byTmdb[id] = it
			}
		}
		for _, title := range []string{it.Name, it.OriginalTitle} {
			t := Normalize(title)
			if t == "" {
				continue
			}
			k := key{title: t, year: yearOf(it)}
			m.byYear[k] = append(m.byYear[k], it)
			m.byTitle[t] = append(m.byTitle[t], it)
		}
	}
	return m
}

// Match resolves a film by title and year.
func (m *Matcher) Match(f letterboxd.Film) *jellyfin.Item {
	title := Normalize(f.Title)
	if title == "" {
		return nil
	}

	if it := first(m.byYear[key{title: title, year: f.Year}]); it != nil {
		return it
	}
	if f.Year > 0 {
		if it := first(m.byYear[key{title: title, year: f.Year - 1}]); it != nil {
			return it
		}
		if it := first(m.byYear[key{title: title, year: f.Year + 1}]); it != nil {
			return it
		}
	}

	// Title alone is only safe when the library holds exactly one such film:
	// otherwise remakes and same-name films would be matched arbitrarily.
	if items := m.byTitle[title]; len(items) == 1 {
		it := items[0]
		return &it
	}
	return nil
}

// MatchTmdb resolves a film by exact TMDB id.
func (m *Matcher) MatchTmdb(id string) *jellyfin.Item {
	if id == "" {
		return nil
	}
	if it, ok := m.byTmdb[id]; ok {
		return &it
	}
	return nil
}

func first(items []jellyfin.Item) *jellyfin.Item {
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

var articles = map[string]struct{}{
	"the": {}, "a": {}, "an": {},
	"le": {}, "la": {}, "les": {}, "un": {}, "une": {}, "du": {}, "des": {},
	"el": {}, "los": {}, "las": {},
	"il": {}, "lo": {}, "gli": {},
	"der": {}, "die": {}, "das": {}, "de": {}, "het": {},
}

var romans = map[string]string{
	"ii": "2", "iii": "3", "iv": "4", "v": "5", "vi": "6",
	"vii": "7", "viii": "8", "ix": "9", "x": "10",
}

// Normalize reduces a title to a comparison key: lowercase, diacritics folded,
// punctuation dropped, leading article removed, trailing roman numerals
// converted. "Amélie" and "Amelie" collapse together, as do "Part II" and
// "Part 2".
func Normalize(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = norm.NFD.String(s)

	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue // combining marks left over from diacritics
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		if r == '&' {
			b.WriteString(" and ")
			continue
		}
		b.WriteRune(' ')
	}

	fields := strings.Fields(b.String())
	if len(fields) > 1 {
		if _, ok := articles[fields[0]]; ok {
			fields = fields[1:]
		}
	}
	for i := 1; i < len(fields); i++ {
		if n, ok := romans[fields[i]]; ok {
			fields[i] = n
		}
	}
	return strings.Join(fields, " ")
}
