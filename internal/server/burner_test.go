package server

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"watchlist-burner/internal/jellyfin"
	"watchlist-burner/internal/letterboxd"
)

type fakeLibrary struct {
	movies   []jellyfin.Item
	err      error
	gotSess  *jellyfin.Session
	gotYears []int
}

func (f *fakeLibrary) Movies(ctx context.Context, sess *jellyfin.Session, years []int, onItem ProgressFunc) ([]jellyfin.Item, error) {
	f.gotSess = sess
	f.gotYears = years
	if f.err != nil {
		return nil, f.err
	}
	for i, it := range f.movies {
		if onItem != nil {
			onItem(i, len(f.movies), it)
		}
	}
	return f.movies, nil
}

type fakeScraper struct {
	films []letterboxd.Film
	err   error
}

func (s *fakeScraper) Watchlist(ctx context.Context, username string) ([]letterboxd.Film, error) {
	return s.films, s.err
}

func newTestBurner(lib Library, scr WatchlistSource) *Burner {
	b := NewBurner(lib, scr, time.Hour)
	b.session = &jellyfin.Session{AccessToken: "tok", UserID: "u", Username: "user", JellyfinURL: "http://jf:8096"}
	return b
}

func TestBurnPipeline(t *testing.T) {
	lib := &fakeLibrary{movies: []jellyfin.Item{
		item("j1", "Dune", 2021, false),
		item("j2", "The Matrix", 1999, true),
	}}
	scr := &fakeScraper{films: []letterboxd.Film{
		film("Dune", 2021),
		film("The Matrix", 1999),
		film("Blade Runner", 1982),
	}}
	b := newTestBurner(lib, scr)

	resp, err := b.Burn(context.Background(), "lbuser", nil)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Total != 3 {
		t.Errorf("total = %d, want 3", resp.Total)
	}
	if resp.Matched != 2 {
		t.Errorf("matched = %d, want 2", resp.Matched)
	}
	if resp.Watched != 1 {
		t.Errorf("watched = %d, want 1", resp.Watched)
	}
	if len(resp.Films) != 2 {
		t.Fatalf("films = %d, want 2 (watched hidden)", len(resp.Films))
	}

	if !resp.Films[0].InLibrary || resp.Films[0].Slug != "Dune" {
		t.Errorf("first film = %+v, want in-library Dune", resp.Films[0])
	}
	if resp.Films[1].InLibrary || resp.Films[1].Slug != "Blade Runner" {
		t.Errorf("second film = %+v, want not-in-library Blade Runner", resp.Films[1])
	}
}

func TestBurnFiltersByYearsWhenAllKnown(t *testing.T) {
	lib := &fakeLibrary{movies: []jellyfin.Item{item("j1", "Dune", 1984, false)}}
	scr := &fakeScraper{films: []letterboxd.Film{
		film("Dune", 1984),
		film("Dune", 1984),
		film("Alien", 1979),
	}}
	b := newTestBurner(lib, scr)

	if _, err := b.Burn(context.Background(), "lbuser", nil); err != nil {
		t.Fatal(err)
	}
	want := []int{1979, 1984}
	if !reflect.DeepEqual(lib.gotYears, want) {
		t.Errorf("years = %v, want %v", lib.gotYears, want)
	}
}

func TestBurnFetchesAllWhenYearUnknown(t *testing.T) {
	lib := &fakeLibrary{movies: []jellyfin.Item{item("j1", "Dune", 1984, false)}}
	scr := &fakeScraper{films: []letterboxd.Film{
		film("Dune", 0),
		film("Alien", 1979),
	}}
	b := newTestBurner(lib, scr)

	resp, err := b.Burn(context.Background(), "lbuser", nil)
	if err != nil {
		t.Fatal(err)
	}
	if lib.gotYears != nil {
		t.Errorf("years = %v, want nil (full fetch when a year is unknown)", lib.gotYears)
	}
	if len(resp.Films) != 2 {
		t.Fatalf("films = %d, want 2", len(resp.Films))
	}
	for _, f := range resp.Films {
		if f.Slug == "Dune" && !f.InLibrary {
			t.Errorf("Dune with unknown year should match by title, got %+v", f)
		}
		if f.Slug == "Alien" && f.InLibrary {
			t.Errorf("Alien should not be in library, got %+v", f)
		}
	}
}

func TestBurnServesWarmCacheWithoutRefetch(t *testing.T) {
	lib := &fakeLibrary{movies: []jellyfin.Item{item("j1", "Dune", 2021, false)}}
	scr := &fakeScraper{films: []letterboxd.Film{film("Dune", 2021)}}
	b := newTestBurner(lib, scr)

	if _, err := b.Burn(context.Background(), "lbuser", nil); err != nil {
		t.Fatal(err)
	}

	scr.films = nil
	lib.movies = nil
	resp, err := b.Burn(context.Background(), "lbuser", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Films) != 1 {
		t.Errorf("cache miss: films = %d, want 1 from cache", len(resp.Films))
	}
}

func TestBurnClearsSessionOnExpiry(t *testing.T) {
	lib := &fakeLibrary{err: jellyfin.ErrSessionExpired}
	scr := &fakeScraper{films: []letterboxd.Film{film("Dune", 2021)}}
	b := newTestBurner(lib, scr)

	_, err := b.Burn(context.Background(), "lbuser", nil)
	if !errors.Is(err, jellyfin.ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired", err)
	}
	b.mu.RLock()
	sess := b.session
	b.mu.RUnlock()
	if sess != nil {
		t.Error("session not cleared after expiry")
	}
}

func TestBurnRequiresSession(t *testing.T) {
	b := NewBurner(&fakeLibrary{}, &fakeScraper{}, time.Hour)
	if _, err := b.Burn(context.Background(), "lbuser", nil); !errors.Is(err, jellyfin.ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired for nil session", err)
	}
}

func TestBurnReportsProgress(t *testing.T) {
	lib := &fakeLibrary{movies: []jellyfin.Item{
		item("j1", "Dune", 2021, false),
		item("j2", "Alien", 1979, false),
	}}
	scr := &fakeScraper{films: []letterboxd.Film{film("Dune", 2021)}}
	b := newTestBurner(lib, scr)

	var indexes []int
	var total int
	_, err := b.Burn(context.Background(), "lbuser", func(index, t int, it jellyfin.Item) {
		indexes = append(indexes, index)
		total = t
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{0, 1}; !reflect.DeepEqual(indexes, want) {
		t.Errorf("indexes = %v, want %v", indexes, want)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
}

func TestDistinctYears(t *testing.T) {
	films := []letterboxd.Film{film("A", 2001), film("B", 0), film("C", 2001), film("D", 1999)}
	got := distinctYears(films)
	want := []int{1999, 2001}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("distinctYears = %v, want %v", got, want)
	}
}
