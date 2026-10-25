package admin

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"duplex/internal/config"
)

type credential struct {
	Salt       []byte `json:"salt"`
	Hash       []byte `json:"hash"`
	Iterations int    `json:"iterations"`
}
type session struct {
	CSRF    string
	Expires time.Time
}
type attempt struct {
	Count int
	Reset time.Time
}

// Auth owns the administrator credential, expiring sessions, and login rate limits.
type Auth struct {
	mu         sync.Mutex
	credential credential
	sessions   map[[32]byte]session
	attempts   map[string]attempt
}

func randomToken() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("system entropy unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// InitCredentials creates a password and protected credential files once.
func InitCredentials(dir string) (string, error) {
	if e := os.MkdirAll(dir, 0o700); e != nil {
		return "", e
	}
	path := filepath.Join(dir, "admin.json")
	if _, e := os.Lstat(path); !os.IsNotExist(e) {
		return "", errors.New("administrator credentials already exist")
	}
	password := randomToken()
	salt := make([]byte, 32)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	hash, e := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(credential{Salt: salt, Hash: hash, Iterations: 600000})
	if e != nil {
		return "", e
	}
	if e = config.AtomicWrite(path, b, 0o600); e != nil {
		return "", e
	}
	initial := filepath.Join(dir, "initial-admin-password")
	if e = config.AtomicWrite(initial, []byte(password+"\n"), 0o600); e != nil {
		return "", e
	}
	return initial, nil
}

// OpenAuth loads the credential after checking its file permissions and work factor.
func OpenAuth(path string) (*Auth, error) {
	info, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("administrator credential file must have mode 0600")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var c credential
	if json.Unmarshal(b, &c) != nil || len(c.Salt) != 32 || len(c.Hash) != 32 || c.Iterations < 600000 || c.Iterations > 2000000 {
		return nil, errors.New("invalid administrator credentials")
	}
	return &Auth{credential: c, sessions: map[[32]byte]session{}, attempts: map[string]attempt{}}, nil
}
func secureCookie(r *http.Request) bool { return r.TLS != nil }

// LoginCSRF creates or reuses the cookie-bound token for a sign-in form.
func (a *Auth) LoginCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, e := r.Cookie("duplex_login_csrf"); e == nil && len(c.Value) == 43 {
		return c.Value
	}
	token := randomToken()
	//nolint:gosec // G124: Local HTTP installs need non-Secure cookies; TLS requests always set Secure. HttpOnly and SameSite remain mandatory.
	http.SetCookie(w, &http.Cookie{Name: "duplex_login_csrf", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r), MaxAge: 600})
	return token
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

func equal(a, b string) bool {
	return len(a) > 0 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Login verifies the password, rate limit, and CSRF token before creating a session.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request, username, password, csrf string) error {
	cookie, e := r.Cookie("duplex_login_csrf")
	if e != nil || !sameOrigin(r) || !equal(cookie.Value, csrf) {
		return errors.New("invalid sign-in request; reload the sign-in page")
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return errors.New("invalid sign-in address")
	}
	a.mu.Lock()
	now := time.Now()
	v := a.attempts[host]
	if now.After(v.Reset) {
		v = attempt{Reset: now.Add(time.Minute)}
	}
	if v.Count >= 5 {
		a.mu.Unlock()
		return errors.New("too many sign-in attempts; wait before trying again")
	}
	v.Count++
	if len(a.attempts) > 1024 {
		for k, v := range a.attempts {
			if now.After(v.Reset) {
				delete(a.attempts, k)
			}
		}
	}
	a.attempts[host] = v
	a.mu.Unlock()
	hash, e := pbkdf2.Key(sha256.New, password, a.credential.Salt, a.credential.Iterations, 32)
	if e != nil || !equal(username, "admin") || subtle.ConstantTimeCompare(hash, a.credential.Hash) != 1 {
		return errors.New("incorrect administrator name or password")
	}
	token := randomToken()
	a.mu.Lock()
	for k, s := range a.sessions {
		if now.After(s.Expires) {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= 32 {
		a.mu.Unlock()
		return errors.New("too many active sessions")
	}
	a.sessions[sha256.Sum256([]byte(token))] = session{CSRF: randomToken(), Expires: now.Add(12 * time.Hour)}
	a.mu.Unlock()
	//nolint:gosec // G124: Local HTTP installs need non-Secure cookies; TLS requests always set Secure. HttpOnly and SameSite remain mandatory.
	http.SetCookie(w, &http.Cookie{Name: "duplex_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r), MaxAge: 43200})
	return nil
}

// Session returns an unexpired session associated with the request cookie.
func (a *Auth) Session(r *http.Request) (session, bool) {
	c, e := r.Cookie("duplex_session")
	if e != nil {
		return session{}, false
	}
	key := sha256.Sum256([]byte(c.Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[key]
	if !ok || time.Now().After(s.Expires) {
		delete(a.sessions, key)
		return session{}, false
	}
	return s, true
}

// Authorize checks the session and requires CSRF protection for mutations.
func (a *Auth) Authorize(r *http.Request) (session, error) {
	s, ok := a.Session(r)
	if !ok {
		return s, errors.New("sign in is required")
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
		csrf := r.Header.Get("X-CSRF-Token")
		if csrf == "" {
			csrf = r.FormValue("csrf")
		}
		if !sameOrigin(r) || !equal(csrf, s.CSRF) {
			return s, errors.New("invalid CSRF token; reload the page")
		}
	}
	return s, nil
}

// Logout revokes the current session and expires its browser cookie.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("duplex_session"); e == nil {
		a.mu.Lock()
		delete(a.sessions, sha256.Sum256([]byte(c.Value)))
		a.mu.Unlock()
	}
	//nolint:gosec // G124: Local HTTP installs need non-Secure cookies; TLS requests always set Secure. HttpOnly and SameSite remain mandatory.
	http.SetCookie(w, &http.Cookie{Name: "duplex_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureCookie(r)})
}
