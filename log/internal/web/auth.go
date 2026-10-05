package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie   = "log_session"
	sessionLifetime = 365 * 24 * time.Hour
)

// Auth implements the single-password login with an HMAC-signed session
// cookie. There is no server-side session state; rotating the secret logs
// out every device.
type Auth struct {
	PasswordHash []byte
	Secret       []byte
	Now          func() time.Time
	// Rotate, if set, replaces the stored secret and returns the new
	// signing key (for "Log out everywhere").
	Rotate func() ([]byte, error)

	mu      sync.RWMutex // guards Secret once serving
	limiter rateLimiter
}

// LogOutEverywhere invalidates every session by switching to a new secret.
func (a *Auth) LogOutEverywhere() error {
	key, err := a.Rotate()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.Secret = key
	a.mu.Unlock()
	return nil
}

func (a *Auth) sign(expiry int64) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	m := hmac.New(sha256.New, a.Secret)
	m.Write([]byte("session:" + strconv.FormatInt(expiry, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *Auth) newSession() (string, time.Time) {
	exp := a.Now().Add(sessionLifetime)
	return strconv.FormatInt(exp.Unix(), 10) + "." + a.sign(exp.Unix()), exp
}

func (a *Auth) valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	expStr, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || a.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(a.sign(exp)))
}

// Require redirects unauthenticated requests to the login page.
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.valid(r) {
			if r.Method != http.MethodGet {
				http.Error(w, "not logged in", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CheckPassword verifies a login attempt, applying the per-IP rate limit.
// It returns false, true if the client is rate limited.
func (a *Auth) CheckPassword(r *http.Request, password string) (ok, limited bool) {
	ip := clientIP(r)
	if !a.limiter.allow(ip, a.Now()) {
		return false, true
	}
	if bcrypt.CompareHashAndPassword(a.PasswordHash, []byte(password)) != nil {
		a.limiter.fail(ip, a.Now())
		return false, false
	}
	return true, false
}

func (a *Auth) SetSession(w http.ResponseWriter, r *http.Request) {
	value, exp := a.newSession()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/", Expires: exp,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) ClearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// clientIP is the remote address, or the last X-Forwarded-For hop when the
// request came through a local reverse proxy (the front Caddy).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}

// safeNext only allows local paths as post-login redirects.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// rateLimiter allows at most maxFailures failed logins per IP per window.
type rateLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	maxFailures = 5
	window      = time.Minute
)

func (l *rateLimiter) recent(ip string, now time.Time) []time.Time {
	var kept []time.Time
	for _, t := range l.failures[ip] {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
	} else {
		l.failures[ip] = kept
	}
	return kept
}

func (l *rateLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = map[string][]time.Time{}
	}
	return len(l.recent(ip, now)) < maxFailures
}

func (l *rateLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = map[string][]time.Time{}
	}
	l.failures[ip] = append(l.recent(ip, now), now)
}
