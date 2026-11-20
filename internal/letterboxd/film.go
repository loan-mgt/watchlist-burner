// Package letterboxd scrapes a user's Letterboxd watchlist. Letterboxd has no
// public API (the v0 API is Cloudflare-protected for server-side use), so the
// HTML watchlist is parsed directly. Requests are paced and retried to stay a
// polite client on a public deployment.
package letterboxd

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Film is a single entry scraped from a Letterboxd watchlist page.
type Film struct {
	Slug      string
	Title     string
	Year      int
	PosterURL string
	URL       string
}

// ErrWatchlistNotFound is returned when the user has no watchlist or does not
// exist.
var ErrWatchlistNotFound = errors.New("letterboxd: watchlist not found")

var (
	fullNameRe = regexp.MustCompile(`^(.*?)\s+\((\d{4})\)$`)
	uidRe      = regexp.MustCompile(`"uid":"film:(\d+)"`)
	userRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// splitName turns "The Game (1997)" into ("The Game", 1997).
func splitName(fullName string) (string, int) {
	if m := fullNameRe.FindStringSubmatch(fullName); len(m) == 3 {
		if year, err := strconv.Atoi(m[2]); err == nil {
			return strings.TrimSpace(m[1]), year
		}
	}
	return strings.TrimSpace(fullName), 0
}

// posterURL builds the Letterboxd CDN poster URL from the internal film id and
// slug: /film-poster/{each digit}/{id}-{slug}-0-125-0-187-crop.jpg
func posterURL(uid, slug string) string {
	digits := make([]string, 0, len(uid))
	for _, r := range uid {
		digits = append(digits, string(r))
	}
	return fmt.Sprintf("%s/film-poster/%s/%s-%s-0-125-0-187-crop.jpg",
		posterBaseURL, strings.Join(digits, "/"), uid, slug)
}

// ValidUsername reports whether a Letterboxd username is safe to put in a URL.
func ValidUsername(name string) bool { return userRe.MatchString(name) }
