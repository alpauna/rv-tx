// Command controlplane runs the rv-tx control plane: Postgres-backed
// mesh coordination over WebSocket, Traefik dynamic config generation,
// and a dashboard (API + embedded SPA) for managing resources. Config
// entirely from env vars, matching the .env convention already used
// elsewhere in this project.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"time"

	"rv-tx/dashboard"
	"rv-tx/internal/controlplane/auth"
	"rv-tx/internal/controlplane/db"
	"rv-tx/internal/controlplane/mail"
	"rv-tx/internal/controlplane/traefikconfig"
	"rv-tx/internal/controlplane/wsserver"
)

func main() {
	dsn := requireEnv("RVTX_POSTGRES_DSN")
	meshCIDR := requireEnv("RVTX_MESH_CIDR")
	listenAddr := envOr("RVTX_LISTEN_ADDR", ":8080")
	migrationsDir := envOr("RVTX_MIGRATIONS_DIR", "migrations")
	sessionSecret := requireEnv("RVTX_SESSION_SECRET")
	baseURL := requireEnv("RVTX_DASHBOARD_BASE_URL")

	smtpCfg := mail.Config{
		Server:   os.Getenv("RVTX_SMTP_SERVER"),
		Port:     envOr("RVTX_SMTP_PORT", "587"),
		User:     os.Getenv("RVTX_SMTP_USER"),
		Password: os.Getenv("RVTX_SMTP_PASSWORD"),
		From:     os.Getenv("RVTX_SMTP_FROM"),
	}
	if !smtpCfg.Configured() {
		log.Printf("warning: RVTX_SMTP_SERVER not set -- invite/reset emails will fail to send")
	}

	ctx := context.Background()

	database, err := db.Connect(ctx, dsn)
	if err != nil {
		log.Fatalf("connect to postgres: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(ctx, migrationsDir); err != nil {
		log.Fatalf("run migrations: %v", err)
	}
	log.Printf("migrations applied")

	if err := bootstrapAdmin(ctx, database); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}

	srv := wsserver.New(database, meshCIDR)

	mux := http.NewServeMux()

	// Agent/Traefik-facing -- deliberately unauthenticated, must keep
	// working regardless of the dashboard's own auth below. Agents
	// connect over WireGuard-adjacent trust already (mesh membership
	// itself is the control), and Traefik's own HTTP provider has no
	// mechanism to send credentials.
	mux.HandleFunc("/ws/agent", srv.HandleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/traefik/config", traefikConfigHandler(database))

	// Dashboard API -- unauthenticated: login itself, and the
	// invite-accept/forgot-password/reset-password flows (a user
	// following an emailed link isn't logged in yet by definition).
	mux.HandleFunc("POST /api/login", loginHandler(database, sessionSecret))
	mux.HandleFunc("POST /api/logout", logoutHandler())
	mux.HandleFunc("GET /api/invite-info", inviteInfoHandler(database))
	mux.HandleFunc("POST /api/accept-invite", acceptInviteHandler(database))
	mux.HandleFunc("POST /api/forgot-password", forgotPasswordHandler(database, smtpCfg, baseURL))
	mux.HandleFunc("POST /api/reset-password", resetPasswordHandler(database))

	// Authenticated, any role (read-only for a viewer).
	mux.Handle("GET /api/whoami", requireAuth(sessionSecret, whoamiHandler()))
	mux.Handle("GET /api/nodes", requireAuth(sessionSecret, listNodesHandler(database)))
	mux.Handle("GET /api/resources", requireAuth(sessionSecret, listResourcesHandler(database)))

	// Authenticated, admin only (mutations).
	mux.Handle("POST /api/resources", requireAdmin(sessionSecret, createResourceHandler(database)))
	mux.Handle("DELETE /api/resources/{name}", requireAdmin(sessionSecret, deleteResourceHandler(database)))
	mux.Handle("GET /api/users", requireAdmin(sessionSecret, listUsersHandler(database)))
	mux.Handle("POST /api/users", requireAdmin(sessionSecret, inviteUserHandler(database, smtpCfg, baseURL)))
	mux.Handle("DELETE /api/users/{email}", requireAdmin(sessionSecret, deleteUserHandler(database)))
	mux.Handle("PUT /api/users/{email}/role", requireAdmin(sessionSecret, setUserRoleHandler(database)))

	// The dashboard SPA itself -- everything not matched above.
	mux.Handle("/", spaHandler())

	log.Printf("control plane listening on %s (mesh cidr %s)", listenAddr, meshCIDR)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

// bootstrapAdmin creates the very first user account, once, only when
// the users table is genuinely empty -- every account after that goes
// through the normal admin-invite flow. Reuses the legacy
// RVTX_DASHBOARD_PASSWORD_HASH/RVTX_BOOTSTRAP_ADMIN_EMAIL env vars so
// a password already in the operator's hands (from before per-user
// accounts existed) keeps working as the first admin's login, rather
// than forcing a fresh invite email to bootstrap the very account
// that would need to send it.
func bootstrapAdmin(ctx context.Context, database *db.DB) error {
	n, err := database.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	email := requireEnv("RVTX_BOOTSTRAP_ADMIN_EMAIL")
	hash := requireEnv("RVTX_DASHBOARD_PASSWORD_HASH")
	if err := database.CreateAdminWithPassword(ctx, email, hash); err != nil {
		return err
	}
	log.Printf("bootstrapped initial admin account %s", email)
	return nil
}

// sessionHandler is an http handler that already has a validated
// session -- every authenticated route is written against this
// instead of the raw net/http signature, so a handler that forgets to
// check the session simply can't compile without one.
type sessionHandler func(w http.ResponseWriter, r *http.Request, sess auth.Session)

func requireAuth(sessionSecret string, next sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := auth.ValidSession(r, sessionSecret)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r, sess)
	})
}

func requireAdmin(sessionSecret string, next sessionHandler) http.Handler {
	return requireAuth(sessionSecret, func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if !sess.IsAdmin() {
			http.Error(w, "forbidden: admin role required", http.StatusForbidden)
			return
		}
		next(w, r, sess)
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func loginHandler(database *db.DB, sessionSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		user, hash, ok, err := database.UserByEmail(r.Context(), req.Email)
		if err != nil {
			log.Printf("login lookup %q: %v", req.Email, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok || !auth.VerifyPassword(hash, req.Password) {
			http.Error(w, "invalid email or password", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, auth.NewSessionCookie(sessionSecret, user.Email, user.Role))
		w.WriteHeader(http.StatusOK)
	}
}

func logoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, auth.ExpiredCookie())
		w.WriteHeader(http.StatusOK)
	}
}

// traefikConfigHandler serves Traefik's dynamic config JSON -- this is
// what Traefik's `http` provider polls (traefik.yml:
// `providers.http.endpoint`), and what the dashboard's config-preview
// page fetches directly (no separate authenticated endpoint needed --
// this one is already meant to be machine-readable within the LAN
// trust model).
func traefikConfigHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resources, err := database.ListResourcesWithTargets(r.Context())
		if err != nil {
			log.Printf("list resources for traefik config: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		cfg := traefikconfig.Generate(resources)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(cfg); err != nil {
			log.Printf("encode traefik config: %v", err)
		}
	}
}

// whoamiHandler lets the SPA learn who's logged in (and with what
// role) on page load/refresh, since the session lives in an HttpOnly
// cookie the JS side can't read directly.
func whoamiHandler() sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}{Email: sess.Email, Role: sess.Role})
	}
}

func listNodesHandler(database *db.DB) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		nodes, err := database.ListNodes(r.Context())
		if err != nil {
			log.Printf("list nodes: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(nodes); err != nil {
			log.Printf("encode nodes: %v", err)
		}
	}
}

func listResourcesHandler(database *db.DB) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		resources, err := database.ListResourcesWithTargets(r.Context())
		if err != nil {
			log.Printf("list resources: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resources); err != nil {
			log.Printf("encode resources: %v", err)
		}
	}
}

// createResourceRequest is the POST /api/resources body. Targets is a
// list rather than a single target: http resources can have any
// number (a load-balanced pool), tcp/udp resources at most a primary
// and a backup (validated in db.CreateResource).
type createResourceRequest struct {
	Name             string           `json:"name"`
	Protocol         string           `json:"protocol"`
	Domain           string           `json:"domain,omitempty"`
	EntryPoint       string           `json:"entry_point"`
	CertResolver     string           `json:"cert_resolver,omitempty"`      // http only -- name of a Traefik certificatesResolvers entry (static config)
	TargetScheme     string           `json:"target_scheme,omitempty"`      // http only -- "http" (default) or "https", for a backend that's itself HTTPS-only (e.g. Proxmox's own web UI)
	TargetSkipVerify bool             `json:"target_skip_verify,omitempty"` // http+https only -- skip TLS verification for a self-signed backend cert
	Targets          []targetSpecJSON `json:"targets"`
}

// targetSpecJSON: exactly one of NodeName or Address must be set --
// NodeName resolves to an rv-tx mesh node's live mesh_ip, Address is
// used as-is for a target that isn't a mesh member (e.g. a raw LAN IP
// already reachable from wherever Traefik runs).
type targetSpecJSON struct {
	NodeName string `json:"node_name,omitempty"`
	Address  string `json:"address,omitempty"`
	Port     int    `json:"port"`
	Role     string `json:"role,omitempty"` // "primary" or "backup"; omit for http (ignored) or a single tcp/udp target (defaults to "primary")
}

func createResourceHandler(database *db.DB) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var req createResourceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body: "+err.Error(), http.StatusBadRequest)
			return
		}

		var domain *string
		if req.Domain != "" {
			domain = &req.Domain
		}
		var certResolver *string
		if req.CertResolver != "" {
			certResolver = &req.CertResolver
		}

		targets := make([]db.TargetSpec, len(req.Targets))
		for i, t := range req.Targets {
			targets[i] = db.TargetSpec{NodeName: t.NodeName, Address: t.Address, Port: t.Port, Role: t.Role}
		}

		err := database.CreateResource(r.Context(), req.Name, req.Protocol, domain, req.EntryPoint, certResolver, req.TargetScheme, req.TargetSkipVerify, targets)
		if err != nil {
			log.Printf("create resource %q: %v", req.Name, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusCreated)
	}
}

func deleteResourceHandler(database *db.DB) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		name := r.PathValue("name")
		if err := database.DeleteResource(r.Context(), name); err != nil {
			log.Printf("delete resource %q: %v", name, err)
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func validRole(role string) bool {
	return role == auth.RoleAdmin || role == auth.RoleViewer
}

func listUsersHandler(database *db.DB) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		users, err := database.ListUsers(r.Context())
		if err != nil {
			log.Printf("list users: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(users); err != nil {
			log.Printf("encode users: %v", err)
		}
	}
}

type inviteUserRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func inviteUserHandler(database *db.DB, smtpCfg mail.Config, baseURL string) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var req inviteUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if !emailRe.MatchString(req.Email) {
			http.Error(w, "invalid email address", http.StatusBadRequest)
			return
		}
		if !validRole(req.Role) {
			http.Error(w, "role must be \"admin\" or \"viewer\"", http.StatusBadRequest)
			return
		}

		token, err := auth.RandomToken()
		if err != nil {
			log.Printf("generate invite token: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := database.CreateInvite(r.Context(), req.Email, req.Role, token); err != nil {
			log.Printf("create invite %q: %v", req.Email, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		link := baseURL + "/accept-invite?token=" + token
		body := "You've been invited to the rv-tx dashboard as a " + req.Role + ".\n\nSet your password here (link expires in 7 days):\n" + link + "\n"
		if err := mail.Send(smtpCfg, req.Email, "rv-tx dashboard invite", body); err != nil {
			log.Printf("send invite email to %q: %v", req.Email, err)
			http.Error(w, "invite created but the email failed to send: "+err.Error(), http.StatusBadGateway)
			return
		}

		w.WriteHeader(http.StatusCreated)
	}
}

func deleteUserHandler(database *db.DB) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		email := r.PathValue("email")
		if email == sess.Email {
			http.Error(w, "cannot delete your own account", http.StatusBadRequest)
			return
		}
		if err := database.DeleteUser(r.Context(), email); err != nil {
			log.Printf("delete user %q: %v", email, err)
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type setRoleRequest struct {
	Role string `json:"role"`
}

func setUserRoleHandler(database *db.DB) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		email := r.PathValue("email")
		if email == sess.Email {
			http.Error(w, "cannot change your own role", http.StatusBadRequest)
			return
		}
		var req setRoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if !validRole(req.Role) {
			http.Error(w, "role must be \"admin\" or \"viewer\"", http.StatusBadRequest)
			return
		}
		if err := database.SetUserRole(r.Context(), email, req.Role); err != nil {
			log.Printf("set role for %q: %v", email, err)
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// inviteInfoHandler lets the accept-invite page show who/what role a
// token belongs to before asking for a password, and reject a
// dead/expired link immediately instead of after a wasted form fill.
func inviteInfoHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		user, ok, err := database.UserByInviteToken(r.Context(), token)
		if err != nil {
			log.Printf("invite info lookup: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "invite link is invalid or expired", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}{Email: user.Email, Role: user.Role})
	}
}

type acceptInviteRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func acceptInviteHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req acceptInviteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if len(req.Password) < 8 {
			http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			log.Printf("hash password: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := database.AcceptInvite(r.Context(), req.Token, hash); err != nil {
			http.Error(w, "invite link is invalid or expired", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

func forgotPasswordHandler(database *db.DB, smtpCfg mail.Config, baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req forgotPasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}

		token, err := auth.RandomToken()
		if err != nil {
			log.Printf("generate reset token: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		existed, err := database.CreateResetToken(r.Context(), req.Email, token)
		if err != nil {
			log.Printf("create reset token: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Same 200 response whether or not the email is registered --
		// don't let this endpoint be used to enumerate accounts.
		if existed {
			link := baseURL + "/reset-password?token=" + token
			body := "A password reset was requested for your rv-tx dashboard account.\n\nReset it here (link expires in 1 hour):\n" + link + "\n\nIf you didn't request this, ignore this email.\n"
			if err := mail.Send(smtpCfg, req.Email, "rv-tx dashboard password reset", body); err != nil {
				log.Printf("send reset email to %q: %v", req.Email, err)
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}

type resetPasswordRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func resetPasswordHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req resetPasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if len(req.Password) < 8 {
			http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			log.Printf("hash password: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := database.ResetPassword(r.Context(), req.Token, hash); err != nil {
			http.Error(w, "reset link is invalid or expired", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// spaHandler serves the embedded, built dashboard (dashboard/dist).
// Real files (JS/CSS/asset requests) are served as-is; anything else
// falls back to index.html's content so client-side routing works on
// a direct URL load or a page refresh -- e.g. /accept-invite?token=...
// from an emailed link, not just "/".
func spaHandler() http.Handler {
	sub, err := fs.Sub(dashboard.DistFS, "dist")
	if err != nil {
		log.Fatalf("dashboard embed: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))

	indexHTML, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		log.Fatalf("dashboard embed: read index.html: %v", err)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if _, err := fs.Stat(sub, path[1:]); err != nil {
			// Serve index.html's bytes directly rather than rewriting
			// the request path to "/index.html" and re-invoking
			// http.FileServer -- FileServer treats a literal
			// "/index.html" request specially (301-redirects it to
			// "./"), and that redirect's relative Location resolves
			// against the ORIGINAL request path, not root. For a
			// multi-segment path like "/accept-invite" that silently
			// landed back at "/" instead of actually serving the SPA
			// there (confirmed live: an invite link bounced straight
			// to the login page instead of the accept-invite form).
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(indexHTML))
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
