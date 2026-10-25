package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionCSRFAndSignOut(t *testing.T) {
	dir := t.TempDir()
	file, e := InitCredentials(dir)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(file)
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
