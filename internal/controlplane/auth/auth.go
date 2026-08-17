// Package auth implements the dashboard's per-user login: bcrypt
// password hashes stored per user in Postgres, and a stateless,
// HMAC-signed session cookie carrying the user's identity and role --
// no server-side session store. The agent/Traefik-facing endpoints
// (/ws/agent, /healthz, /traefik/config) are deliberately untouched by
// any of this.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName = "rvtx_session"
	sessionTTL = 30 * 24 * time.Hour

	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// HashPassword bcrypt-hashes a plaintext password for storage.
func HashPassword(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	return string(h), err
}

// VerifyPassword reports whether plaintext matches a stored bcrypt hash.
func VerifyPassword(hash, plaintext string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// RandomToken returns a random URL-safe token for invite/reset links --
// 32 bytes of entropy, hex-encoded so it's safe to embed directly in a
// URL path with no further escaping.
func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Session is what a valid signed cookie proves: which user, and what
// they're allowed to do.
type Session struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	Exp   int64  `json:"exp"`
}

func (s Session) IsAdmin() bool { return s.Role == RoleAdmin }

// NewSessionCookie returns a signed cookie proving successful login as
// the given user. Stateless (the session payload plus an HMAC
// signature over it) so a control-plane restart doesn't invalidate
// existing sessions and no session table is needed.
func NewSessionCookie(secret, email, role string) *http.Cookie {
	exp := time.Now().Add(sessionTTL).Unix()
	return &http.Cookie{
		Name:     CookieName,
		Value:    signValue(secret, Session{Email: email, Role: role, Exp: exp}),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(exp, 0),
	}
}

// ExpiredCookie clears a session cookie (for logout).
func ExpiredCookie() *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
}

// ValidSession returns the session carried by r's cookie, if it's
// present, correctly signed, and unexpired.
func ValidSession(r *http.Request, secret string) (Session, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Session{}, false
	}
	sess, ok := verifyValue(secret, c.Value)
	if !ok {
		return Session{}, false
	}
	if time.Now().Unix() >= sess.Exp {
		return Session{}, false
	}
	return sess, true
}

func signValue(secret string, sess Session) string {
	raw, _ := json.Marshal(sess)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + base64.RawURLEncoding.EncodeToString(sign(secret, payload))
}

func verifyValue(secret, value string) (Session, bool) {
	payloadB64, sigB64, ok := strings.Cut(value, ".")
	if !ok {
		return Session{}, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return Session{}, false
	}
	if subtle.ConstantTimeCompare(sig, sign(secret, payloadB64)) != 1 {
		return Session{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return Session{}, false
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return Session{}, false
	}
	return sess, true
}

func sign(secret, payload string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}
