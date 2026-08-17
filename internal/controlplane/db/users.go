package db

import (
	"context"
	"fmt"
	"time"
)

const inviteTTL = 7 * 24 * time.Hour
const resetTTL = 1 * time.Hour

// User mirrors the users table. PasswordHash is nil until an invited
// user sets their password; the two token fields are nil outside an
// active invite/reset flow.
type User struct {
	ID             string    `json:"id"`
	Email          string    `json:"email"`
	Role           string    `json:"role"`
	HasSetPassword bool      `json:"has_set_password"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateInvite inserts a new user row with no password set and a
// fresh invite token, or fails if the email is already registered.
// Returns the raw token to embed in the invite email/link -- never
// stored anywhere except this row, and only ever compared, not
// re-displayed.
func (db *DB) CreateInvite(ctx context.Context, email, role, token string) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO users (email, role, invite_token, invite_token_expires)
		VALUES ($1, $2, $3, $4)
	`, email, role, token, time.Now().Add(inviteTTL))
	if err != nil {
		return fmt.Errorf("create invite for %q: %w", email, err)
	}
	return nil
}

// UserByInviteToken looks up the pending invite a token belongs to --
// returns ok=false if the token doesn't exist or has expired (both
// treated identically, so an expired-vs-unknown token can't be
// distinguished by a caller probing tokens).
func (db *DB) UserByInviteToken(ctx context.Context, token string) (User, bool, error) {
	var u User
	err := db.Pool.QueryRow(ctx, `
		SELECT id::text, email, role, (password_hash IS NOT NULL), created_at
		FROM users
		WHERE invite_token = $1 AND invite_token_expires > now()
	`, token).Scan(&u.ID, &u.Email, &u.Role, &u.HasSetPassword, &u.CreatedAt)
	if err != nil {
		return User{}, false, nil
	}
	return u, true, nil
}

// AcceptInvite sets a pending user's password and clears the invite
// token (single use).
func (db *DB) AcceptInvite(ctx context.Context, token, passwordHash string) error {
	tag, err := db.Pool.Exec(ctx, `
		UPDATE users
		SET password_hash = $1, invite_token = NULL, invite_token_expires = NULL
		WHERE invite_token = $2 AND invite_token_expires > now()
	`, passwordHash, token)
	if err != nil {
		return fmt.Errorf("accept invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("invite token not found or expired")
	}
	return nil
}

// UserByEmail looks up a user for login -- returns ok=false (not an
// error) if no such user exists, so callers can give a uniform
// "invalid credentials" response either way.
func (db *DB) UserByEmail(ctx context.Context, email string) (User, string, bool, error) {
	var u User
	var passwordHash *string
	err := db.Pool.QueryRow(ctx, `
		SELECT id::text, email, role, password_hash, created_at
		FROM users
		WHERE email = $1
	`, email).Scan(&u.ID, &u.Email, &u.Role, &passwordHash, &u.CreatedAt)
	if err != nil {
		return User{}, "", false, nil
	}
	u.HasSetPassword = passwordHash != nil
	hash := ""
	if passwordHash != nil {
		hash = *passwordHash
	}
	return u, hash, true, nil
}

// ListUsers returns every user, newest first, for the admin Users page.
func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id::text, email, role, (password_hash IS NOT NULL), created_at
		FROM users
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.HasSetPassword, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// DeleteUser removes a user by email.
func (db *DB) DeleteUser(ctx context.Context, email string) error {
	tag, err := db.Pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, email)
	if err != nil {
		return fmt.Errorf("delete user %q: %w", email, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user %q not found", email)
	}
	return nil
}

// SetUserRole updates an existing user's role.
func (db *DB) SetUserRole(ctx context.Context, email, role string) error {
	tag, err := db.Pool.Exec(ctx, `UPDATE users SET role = $1 WHERE email = $2`, role, email)
	if err != nil {
		return fmt.Errorf("set role for %q: %w", email, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("user %q not found", email)
	}
	return nil
}

// CreateResetToken sets a fresh password-reset token on an existing
// user, if one exists for the given email. Returns ok=false (not an
// error) for an unknown email, so the caller can send an identical
// "check your email" response either way and not leak which emails
// are registered.
func (db *DB) CreateResetToken(ctx context.Context, email, token string) (bool, error) {
	tag, err := db.Pool.Exec(ctx, `
		UPDATE users
		SET reset_token = $1, reset_token_expires = $2
		WHERE email = $3
	`, token, time.Now().Add(resetTTL), email)
	if err != nil {
		return false, fmt.Errorf("create reset token for %q: %w", email, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ResetPassword sets a new password via a valid, unexpired reset
// token and clears the token (single use).
func (db *DB) ResetPassword(ctx context.Context, token, passwordHash string) error {
	tag, err := db.Pool.Exec(ctx, `
		UPDATE users
		SET password_hash = $1, reset_token = NULL, reset_token_expires = NULL
		WHERE reset_token = $2 AND reset_token_expires > now()
	`, passwordHash, token)
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("reset token not found or expired")
	}
	return nil
}

// CountUsers reports how many users exist, so main() can bootstrap an
// initial admin only on a genuinely empty table.
func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// CreateAdminWithPassword inserts the very first user directly with a
// password already set -- used once, at startup, to bootstrap an
// admin account when the users table is empty (see main.go). Every
// user after this one goes through the normal invite flow.
func (db *DB) CreateAdminWithPassword(ctx context.Context, email, passwordHash string) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO users (email, role, password_hash)
		VALUES ($1, 'admin', $2)
	`, email, passwordHash)
	if err != nil {
		return fmt.Errorf("bootstrap admin %q: %w", email, err)
	}
	return nil
}
