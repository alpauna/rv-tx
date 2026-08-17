// Command controlplane runs the rv-tx control plane: Postgres-backed
// mesh coordination over WebSocket, Traefik dynamic config generation,
// and a dashboard (API + embedded SPA) for managing resources. Config
// entirely from env vars, matching the .env convention already used
// elsewhere in this project.
package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"

	"rv-tx/dashboard"
	"rv-tx/internal/controlplane/auth"
	"rv-tx/internal/controlplane/db"
	"rv-tx/internal/controlplane/traefikconfig"
	"rv-tx/internal/controlplane/wsserver"
)

func main() {
	dsn := requireEnv("RVTX_POSTGRES_DSN")
	meshCIDR := requireEnv("RVTX_MESH_CIDR")
	listenAddr := envOr("RVTX_LISTEN_ADDR", ":8080")
	migrationsDir := envOr("RVTX_MIGRATIONS_DIR", "migrations")
	passwordHash := requireEnv("RVTX_DASHBOARD_PASSWORD_HASH")
	sessionSecret := requireEnv("RVTX_SESSION_SECRET")

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

	// Dashboard API -- authenticated except login itself.
	mux.HandleFunc("POST /api/login", loginHandler(passwordHash, sessionSecret))
	mux.HandleFunc("POST /api/logout", logoutHandler())
	mux.Handle("GET /api/nodes", requireAuth(sessionSecret, listNodesHandler(database)))
	mux.Handle("GET /api/resources", requireAuth(sessionSecret, listResourcesHandler(database)))
	mux.Handle("POST /api/resources", requireAuth(sessionSecret, createResourceHandler(database)))
	mux.Handle("DELETE /api/resources/{name}", requireAuth(sessionSecret, deleteResourceHandler(database)))

	// The dashboard SPA itself -- everything not matched above.
	mux.Handle("/", spaHandler())

	log.Printf("control plane listening on %s (mesh cidr %s)", listenAddr, meshCIDR)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

// requireAuth wraps a handler so it 401s without a valid session
// cookie -- used for every /api/* route except POST /api/login.
func requireAuth(sessionSecret string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.ValidSession(r, sessionSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	})
}

type loginRequest struct {
	Password string `json:"password"`
}

func loginHandler(passwordHash, sessionSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if !auth.VerifyPassword(passwordHash, req.Password) {
			http.Error(w, "invalid password", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, auth.NewSessionCookie(sessionSecret))
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

func listNodesHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

func listResourcesHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
	Name         string           `json:"name"`
	Protocol     string           `json:"protocol"`
	Domain       string           `json:"domain,omitempty"`
	EntryPoint   string           `json:"entry_point"`
	CertResolver string           `json:"cert_resolver,omitempty"` // http only -- name of a Traefik certificatesResolvers entry (static config)
	Targets      []targetSpecJSON `json:"targets"`
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

func createResourceHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		err := database.CreateResource(r.Context(), req.Name, req.Protocol, domain, req.EntryPoint, certResolver, targets)
		if err != nil {
			log.Printf("create resource %q: %v", req.Name, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusCreated)
	}
}

func deleteResourceHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := database.DeleteResource(r.Context(), name); err != nil {
			log.Printf("delete resource %q: %v", name, err)
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// spaHandler serves the embedded, built dashboard (dashboard/dist).
// Real files (JS/CSS/asset requests) are served as-is; anything else
// falls back to index.html so client-side routing works on a direct
// URL load or a page refresh.
func spaHandler() http.Handler {
	sub, err := fs.Sub(dashboard.DistFS, "dist")
	if err != nil {
		log.Fatalf("dashboard embed: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if _, err := fs.Stat(sub, path[1:]); err != nil {
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/index.html"
			fileServer.ServeHTTP(w, r2)
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
