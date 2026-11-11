package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tktogether/warden-gateway/internal/config"
)

const (
	DefaultCookieName = "wg_session"
	DefaultTTL        = 12 * time.Hour
)

type Session struct {
	Version int    `json:"v"`
	Issued  int64  `json:"iat"`
	Expires int64  `json:"exp"`
	CSRF    string `json:"csrf"`
	KeyID   string `json:"kid"`
}

type Manager struct {
	mu      sync.RWMutex
	secret  []byte
	keyHash string
	keyID   string
	ttl     time.Duration
	cookie  string
	limiter *LoginLimiter
}

func NewManager(secret, keyHash string, ttl time.Duration) (*Manager, error) {
	if len(secret) < 32 {
		return nil, errors.New("session secret must be at least 32 characters")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	m := &Manager{
		secret:  []byte(secret),
		keyHash: strings.ToLower(strings.TrimSpace(keyHash)),
		ttl:     ttl,
		cookie:  DefaultCookieName,
		limiter: NewLoginLimiter(8, 5*time.Minute),
	}
	m.keyID = KeyID(m.keyHash)
	return m, nil
}

func KeyID(hash string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(hash))))
	return hex.EncodeToString(sum[:])[:16]
}

func (m *Manager) KeyID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.keyID
}

func (m *Manager) KeyHash() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.keyHash
}

func (m *Manager) SetKeyHash(hash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keyHash = strings.ToLower(strings.TrimSpace(hash))
	m.keyID = KeyID(m.keyHash)
}

func (m *Manager) VerifyAccessKey(key string) bool {
	m.mu.RLock()
	want := m.keyHash
	m.mu.RUnlock()
	if want == "" {
		return false
	}
	sum := sha256.Sum256([]byte(key))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (m *Manager) Issue() (string, Session, error) {
	csrfRaw := make([]byte, 24)
	if _, err := rand.Read(csrfRaw); err != nil {
		return "", Session{}, err
	}
	now := time.Now()
	s := Session{
		Version: 1,
		Issued:  now.Unix(),
		Expires: now.Add(m.ttl).Unix(),
		CSRF:    base64.RawURLEncoding.EncodeToString(csrfRaw),
		KeyID:   m.KeyID(),
	}
	token, err := m.sign(s)
	if err != nil {
		return "", Session{}, err
	}
	return token, s, nil
}

func (m *Manager) Verify(r *http.Request) (Session, bool) {
	c, err := r.Cookie(m.cookie)
	if err != nil || c.Value == "" {
		return Session{}, false
	}
	s, err := m.parse(c.Value)
	if err != nil {
		return Session{}, false
	}
	if s.Version != 1 || time.Now().Unix() >= s.Expires || s.KeyID != m.KeyID() {
		return Session{}, false
	}
	return s, true
}

func (m *Manager) CookieName() string { return m.cookie }

func (m *Manager) Cookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     m.cookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(m.ttl),
		MaxAge:   int(m.ttl.Seconds()),
	}
}

func (m *Manager) ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name:     m.cookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	}
}

func (m *Manager) sign(s Session) (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

func (m *Manager) parse(token string) (Session, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Session{}, errors.New("invalid token")
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(want)) {
		return Session{}, errors.New("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Session{}, err
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return Session{}, err
	}
	return s, nil
}

func (m *Manager) Limiter() *LoginLimiter { return m.limiter }

func VerifyAccessKey(key, hash string) bool {
	if len(key) == 0 || len(hash) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(key))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(hash))) == 1
}

func NewAccessKey() (string, string, error) {
	return config.GenerateAccessKey()
}

func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func OriginAllowed(r *http.Request, publicOrigin string) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	if publicOrigin != "" {
		p, err := url.Parse(publicOrigin)
		if err == nil && strings.EqualFold(u.Host, p.Host) {
			return true
		}
	}
	return false
}

type LoginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	attempts map[string][]time.Time
}

func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{max: max, window: window, attempts: make(map[string][]time.Time)}
}

func (l *LoginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	list := l.attempts[key]
	kept := list[:0]
	for _, t := range list {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.attempts[key] = kept
	return len(kept) < l.max
}

func (l *LoginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[key] = append(l.attempts[key], time.Now())
}

func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
