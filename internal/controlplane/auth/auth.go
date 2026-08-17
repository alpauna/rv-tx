// Package auth implements the dashboard's login: a single shared
// password (bcrypt-hashed) and a stateless, HMAC-signed session
// cookie -- no user accounts/roles, no server-side session store.
// Matches the control plane's existing single-operator, LAN-only
// trust model; the agent/Traefik-facing endpoints
// (/ws/agent, /healthz, /traefik/config) are deliberately untouched by
// any of this.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName = "rvtx_session"
	sessionTTL = 30 * 24 * time.Hour
)

// VerifyPassword reports whether plaintext matches the bcrypt hash
// configured via RVTX_DASHBOARD_PASSWORD_HASH.
func VerifyPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// NewSessionCookie returns a signed cookie proving successful login.
// Stateless (an expiry timestamp plus an HMAC signature over it) so a
// control-plane restart doesn't invalidate existing sessions and no
// session table is needed.
func NewSessionCookie(secret string) *http.Cookie {
	exp := time.Now().Add(sessionTTL).Unix()
	return &http.Cookie{
		Name:     CookieName,
		Value:    signValue(secret, exp),
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

// ValidSession reports whether r carries a valid, unexpired session
// cookie signed with secret.
func ValidSession(r *http.Request, secret string) bool {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	exp, ok := verifyValue(secret, c.Value)
	if !ok {
		return false
	}
	return time.Now().Unix() < exp
}

func signValue(secret string, exp int64) string {
	payload := strconv.FormatInt(exp, 10)
	return payload + "." + base64.RawURLEncoding.EncodeToString(sign(secret, payload))
}

func verifyValue(secret, value string) (int64, bool) {
	payload, sigB64, ok := strings.Cut(value, ".")
	if !ok {
		return 0, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return 0, false
	}
	if subtle.ConstantTimeCompare(sig, sign(secret, payload)) != 1 {
		return 0, false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return 0, false
	}
	return exp, true
}

func sign(secret, payload string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}
