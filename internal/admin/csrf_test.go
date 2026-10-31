package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFCookiesWithoutLogin(t *testing.T) {
	for _, origin := range []string{"http://duplex.test", "https://duplex.test"} {
		t.Run(origin, func(t *testing.T) {
			get := httptest.NewRequestWithContext(t.Context(), http.MethodGet, origin+"/settings", nil)
			get.Header.Set("Cookie", "unrelated=retained; "+csrfCookieName+"=invalid")

			response := httptest.NewRecorder()

			ensureCSRF(response, get)

			cookie := response.Result().Cookies()[0]
			if csrfToken(get) != cookie.Value || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure != (get.TLS != nil) {
				t.Fatal("CSRF cookie or rendered token is invalid", cookie)
			}

			if unrelated, err := get.Cookie("unrelated"); err != nil || unrelated.Value != "retained" {
				t.Fatal("CSRF cookie replacement lost an unrelated cookie", err)
			}

			response = httptest.NewRecorder()
			ensureCSRF(response, get)

			if len(response.Result().Cookies()) != 0 || csrfToken(get) != cookie.Value {
				t.Fatal("page reads replaced a valid CSRF token")
			}

			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
				write := httptest.NewRequestWithContext(t.Context(), method, origin+"/api/v1/config", nil)
				write.Header.Set("Origin", origin)
				write.Header.Set("X-CSRF-Token", cookie.Value)

				if checkCSRF(write) == nil {
					t.Fatal("write without its cookie was accepted", method)
				}

				write.AddCookie(cookie)

				if err := checkCSRF(write); err != nil {
					t.Fatal("same-origin write was rejected", method, err)
				}

				write.Header.Set("X-CSRF-Token", "wrong")

				if checkCSRF(write) == nil {
					t.Fatal("mismatched token was accepted", method)
				}

				write.Header.Set("X-CSRF-Token", cookie.Value)
				write.Header.Set("Origin", "https://evil.test")

				if checkCSRF(write) == nil {
					t.Fatal("cross-origin write was accepted", method)
				}

				write.Header.Del("Origin")
				write.Header.Set("Sec-Fetch-Site", "cross-site")

				if checkCSRF(write) == nil {
					t.Fatal("cross-site write was accepted without Origin", method)
				}
			}

			form := url.Values{"csrf": {cookie.Value}}
			write := httptest.NewRequestWithContext(t.Context(), http.MethodPost, origin+"/actions/settings", strings.NewReader(form.Encode()))
			write.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			write.AddCookie(cookie)

			if err := checkCSRF(write); err != nil {
				t.Fatal("HTML form without Origin was rejected", err)
			}
		})
	}

	for _, token := range []string{"", "short", strings.Repeat("!", 43)} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		request.Header.Set("Cookie", csrfCookieName+"="+token)

		if csrfToken(request) != "" {
			t.Fatal("invalid token was accepted")
		}
	}
}

func TestAdministrationWritesRetainCSRFProtectionWithoutLogin(t *testing.T) {
	f := newWorkflowFixture(t)
	f.loadCSRF(t)
	original := f.app.Config.Snapshot()

	for _, test := range []struct {
		path, origin, token string
		hx                  bool
	}{
		{"/api/v1/config", "http://duplex.test", "", false},
		{"/actions/settings", "http://duplex.test", "wrong", false},
		{"/actions/settings", "https://evil.test", f.csrf, true},
	} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://duplex.test"+test.path, nil)
		request.Header.Set("Origin", test.origin)
		request.Header.Set("X-CSRF-Token", test.token)
		request.Header.Set("Cookie", csrfCookieName+"="+f.csrf)

		if test.hx {
			request.Header.Set("HX-Request", "true")
		}

		response := httptest.NewRecorder()
		f.server.Config.Handler.ServeHTTP(response, request)

		if response.Code != http.StatusForbidden || response.Header().Get("Location") != "" || response.Header().Get("HX-Redirect") != "" || !strings.Contains(response.Body.String(), "invalid CSRF token") {
			t.Fatal("invalid write did not return a usable CSRF error", test, response.Code, response.Body.String())
		}
	}

	if f.app.Config.Snapshot().Revision != original.Revision {
		t.Fatal("rejected request changed settings")
	}
}
