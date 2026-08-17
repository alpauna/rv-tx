package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestVerifyPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(string(hash), "correct-horse") {
		t.Error("expected correct password to verify")
	}
	if VerifyPassword(string(hash), "wrong-password") {
		t.Error("expected wrong password to fail verification")
	}
}

func TestSessionCookieRoundTrip(t *testing.T) {
	cookie := NewSessionCookie("test-secret")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)

	if !ValidSession(req, "test-secret") {
		t.Error("expected a freshly issued cookie to be valid")
	}
	if ValidSession(req, "wrong-secret") {
		t.Error("expected validation against the wrong secret to fail")
	}
}

func TestValidSession_NoCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if ValidSession(req, "test-secret") {
		t.Error("expected no session to be invalid")
	}
}

func TestValidSession_TamperedValue(t *testing.T) {
	cookie := NewSessionCookie("test-secret")
	cookie.Value = cookie.Value + "x" // corrupt the signature

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)

	if ValidSession(req, "test-secret") {
		t.Error("expected a tampered cookie value to be invalid")
	}
}

func TestValidSession_Expired(t *testing.T) {
	// Build an already-expired cookie by signing a past timestamp
	// directly, since NewSessionCookie always issues a future expiry.
	expired := signValue("test-secret", time.Now().Add(-time.Hour).Unix())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: expired})

	if ValidSession(req, "test-secret") {
		t.Error("expected an expired session to be invalid")
	}
}
