package letterboxd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

var (
	posterJSON = regexp.MustCompile(`"image":"(https://a\.ltrbxd\.com/resized/[^"]+-0-600-0-900-crop\.jpg[^"]*)"`)
	cropRe     = regexp.MustCompile(`-0-\d+-0-\d+-crop\.jpg`)
)

// PosterResolver resolves a film's poster from its film page. It is the
// fallback path for films whose constructed CDN URL is missing or invalid.
type PosterResolver struct {
	client *http.Client
}

func NewPosterResolver() *PosterResolver {
	return &PosterResolver{client: &http.Client{}}
}

// Resolve returns the small-crop poster URL for a film slug, or "" if the
// film page cannot be fetched or carries no matching image.
func (p *PosterResolver) Resolve(ctx context.Context, slug string) string {
	u := fmt.Sprintf("%s/film/%s/", baseURL, slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	if m := posterJSON.FindSubmatch(body); len(m) == 2 {
		return smallCrop(string(m[1]))
	}
	return ""
}

// smallCrop rewrites a poster URL's crop suffix to a 125x187 thumbnail.
func smallCrop(u string) string {
	return cropRe.ReplaceAllString(u, "-0-125-0-187-crop.jpg")
}
