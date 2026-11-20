package jellyfin

import "net/http"

// Client identity advertised to Jellyfin. Jellyfin 10.9+ reads the standard
// Authorization header; older servers only understand X-Emby-Authorization,
// so both are always sent with the same value.
const (
	clientName    = "WatchlistBurner"
	deviceName    = "Server"
	deviceID      = "watchlist-burner"
	clientVersion = "1.0.0"
)

// Authorization builds the Emby/Jellyfin authorization header value. The token
// is omitted when empty (unauthenticated requests such as /Users/Me probing).
func Authorization(token string) string {
	v := `MediaBrowser Client="` + clientName +
		`", Device="` + deviceName +
		`", DeviceId="` + deviceID +
		`", Version="` + clientVersion + `"`
	if token != "" {
		v += `, Token="` + token + `"`
	}
	return v
}

// setAuth applies the shared authorization headers to a request.
func setAuth(req *http.Request, token string) {
	value := Authorization(token)
	req.Header.Set("Authorization", value)
	req.Header.Set("X-Emby-Authorization", value)
	req.Header.Set("Accept", "application/json")
}
