package source

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AllowedURL permits only resolver-owned Google media and artwork hosts. Public channel input
// never becomes an arbitrary HTTP or subprocess destination.
func AllowedURL(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return false
	}

	h := strings.ToLower(u.Hostname())
	if h == "yt3.googleusercontent.com" {
		return true
	}

	for _, suffix := range []string{"googlevideo.com", "ytimg.com", "ggpht.com", "youtube.com"} {
		if h == suffix || strings.HasSuffix(h, "."+suffix) {
			return true
		}
	}

	return false
}

// HTTPClient permits only supported source hosts and checks redirects against the same rules.
func HTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 || !AllowedURL(r.URL.String()) {
			return errors.New("source redirect rejected")
		}

		return nil
	}}
}
