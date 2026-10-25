package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSessionCSRFAndSignOut(t *testing.T) {
	dir := t.TempDir()
	file, e := InitCredentials(dir)
	if e != nil {
		t.Fatal(e)
	}
	b, err := os.ReadFile(file) //nolint:gosec // G304: This reads credentials created only in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	auth, e := OpenAuth(filepath.Join(dir, "admin.json"))
	if e != nil {
		t.Fatal(e)
	}
	get := httptest.NewRequestWithContext(t.Context(), "GET", "http://duplex.test/login", nil)
	w := httptest.NewRecorder()
	csrf := auth.LoginCSRF(w, get)
	cookie := w.Result().Cookies()[0]
	login := httptest.NewRequestWithContext(t.Context(), "POST", "http://duplex.test/login", nil)
	login.AddCookie(cookie)
	login.Header.Set("Origin", "http://duplex.test")
	w = httptest.NewRecorder()
	if e = auth.Login(w, login, "admin", strings.TrimSpace(string(b)), csrf); e != nil {
		t.Fatal(e)
	}
	sessionCookie := w.Result().Cookies()[0]
	if !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("insecure session cookie")
	}
	post := httptest.NewRequestWithContext(t.Context(), "POST", "http://duplex.test/actions/config", nil)
	post.AddCookie(sessionCookie)
	if _, e = auth.Authorize(post); e == nil {
		t.Fatal("CSRF bypass")
	}
	s, _ := auth.Session(post)
	post.Header.Set("X-CSRF-Token", s.CSRF)
	post.Header.Set("Origin", "https://evil.test")
	if _, e = auth.Authorize(post); e == nil {
		t.Fatal("cross-origin bypass")
	}
	post.Header.Set("Origin", "http://duplex.test")
	if _, e = auth.Authorize(post); e != nil {
		t.Fatal(e)
	}
	auth.Logout(httptest.NewRecorder(), post)
	if _, ok := auth.Session(post); ok {
		t.Fatal("session remains after sign out")
	}
}

func TestLoginRateLimitsHaveBoundedStorage(t *testing.T) {
	auth := &Auth{attempts: map[string]attempt{}}
	now := time.Now()
	for n := range 1024 {
		if err := auth.loginAttempt(strconv.Itoa(n), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := auth.loginAttempt("overflow", now); err == nil || len(auth.attempts) != 1024 {
		t.Fatal("unbounded rate-limit storage")
	}
	for range 4 {
		if err := auth.loginAttempt("0", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := auth.loginAttempt("0", now); err == nil {
		t.Fatal("password retry limit ignored")
	}
	if err := auth.loginAttempt("new", now.Add(2*time.Minute)); err != nil || len(auth.attempts) != 1 {
		t.Fatal("expired limits not released", err)
	}
}
