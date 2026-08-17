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

func TestVerifyPassword_EmptyHash(t *testing.T) {
	// A user who hasn't accepted their invite yet has no password_hash
	// set at all -- must never verify against an empty hash.
	if VerifyPassword("", "anything") {
		t.Error("expected an empty hash to never verify")
	}
}

func TestSessionCookieRoundTrip(t *testing.T) {
	cookie := NewSessionCookie("test-secret", "alice@example.com", RoleAdmin)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)

	sess, ok := ValidSession(req, "test-secret")
	if !ok {
		t.Fatal("expected a freshly issued cookie to be valid")
	}
	if sess.Email != "alice@example.com" || sess.Role != RoleAdmin {
		t.Errorf("got session %+v, want email=alice@example.com role=admin", sess)
	}
	if !sess.IsAdmin() {
		t.Error("expected IsAdmin() true for an admin session")
	}

	if _, ok := ValidSession(req, "wrong-secret"); ok {
		t.Error("expected validation against the wrong secret to fail")
	}
}

func TestSessionCookieRoundTrip_ViewerRole(t *testing.T) {
	cookie := NewSessionCookie("test-secret", "bob@example.com", RoleViewer)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)

	sess, ok := ValidSession(req, "test-secret")
	if !ok {
		t.Fatal("expected a freshly issued cookie to be valid")
	}
	if sess.IsAdmin() {
		t.Error("expected IsAdmin() false for a viewer session")
	}
}

func TestValidSession_NoCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := ValidSession(req, "test-secret"); ok {
		t.Error("expected no session to be invalid")
	}
}

func TestValidSession_TamperedValue(t *testing.T) {
	cookie := NewSessionCookie("test-secret", "alice@example.com", RoleAdmin)
	cookie.Value = cookie.Value + "x" // corrupt the signature

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)

	if _, ok := ValidSession(req, "test-secret"); ok {
		t.Error("expected a tampered cookie value to be invalid")
	}
}

func TestValidSession_RoleTamperedViaReencoding(t *testing.T) {
	// A viewer forging admin by re-signing their own payload should
	// never succeed without the secret -- sanity check that signing a
	// different role produces a different, non-interchangeable cookie
	// rather than e.g. the role living outside the signed payload.
	viewerCookie := NewSessionCookie("test-secret", "eve@example.com", RoleViewer)
	adminCookie := NewSessionCookie("test-secret", "eve@example.com", RoleAdmin)
	if viewerCookie.Value == adminCookie.Value {
		t.Fatal("expected different roles to produce different signed values")
	}
}

func TestValidSession_Expired(t *testing.T) {
	// Build an already-expired cookie by signing a past timestamp
	// directly, since NewSessionCookie always issues a future expiry.
	expired := signValue("test-secret", Session{Email: "alice@example.com", Role: RoleAdmin, Exp: time.Now().Add(-time.Hour).Unix()})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: expired})

	if _, ok := ValidSession(req, "test-secret"); ok {
		t.Error("expected an expired session to be invalid")
	}
}

func TestRandomToken_Unique(t *testing.T) {
	a, err := RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("expected two random tokens to differ")
	}
	if len(a) != 64 { // 32 bytes hex-encoded
		t.Errorf("got token length %d, want 64", len(a))
	}
}
