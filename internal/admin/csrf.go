package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
)

const csrfCookieName = "duplex_csrf"

func csrfToken(r *http.Request) string {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || len(cookie.Value) != 43 {
		return ""
	}

	decoded, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(decoded) != 32 {
		return ""
	}

	return cookie.Value
}

func ensureCSRF(w http.ResponseWriter, r *http.Request) {
	if csrfToken(r) != "" {
		return
	}

	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		panic("system entropy unavailable")
	}

	//nolint:gosec // G124: Local HTTP installs need non-Secure cookies; TLS requests always set Secure. HttpOnly and SameSite remain mandatory.
	cookie := &http.Cookie{Name: csrfCookieName, Value: base64.RawURLEncoding.EncodeToString(random[:]), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil}
	http.SetCookie(w, cookie)

	cookies := r.Cookies()
	r.Header.Del("Cookie")

	for _, existing := range cookies {
		if existing.Name != csrfCookieName {
			r.AddCookie(existing)
		}
	}

	r.AddCookie(cookie)
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	return origin == scheme+"://"+r.Host
}

func checkCSRF(r *http.Request) error {
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = r.FormValue("csrf")
	}

	cookie := csrfToken(r)
	if !sameOrigin(r) || cookie == "" || subtle.ConstantTimeCompare([]byte(token), []byte(cookie)) != 1 {
		return errors.New("invalid CSRF token; reload the page")
	}

	return nil
}
