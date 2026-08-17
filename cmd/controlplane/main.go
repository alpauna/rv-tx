// Command controlplane runs the rv-tx control plane: Postgres-backed
// mesh coordination over WebSocket. Config entirely from env vars,
// matching the .env convention already used elsewhere in this project.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"rv-tx/internal/controlplane/db"
	"rv-tx/internal/controlplane/traefikconfig"
	"rv-tx/internal/controlplane/wsserver"
)

func main() {
	dsn := requireEnv("RVTX_POSTGRES_DSN")
	meshCIDR := requireEnv("RVTX_MESH_CIDR")
	listenAddr := envOr("RVTX_LISTEN_ADDR", ":8080")
	migrationsDir := envOr("RVTX_MIGRATIONS_DIR", "migrations")

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
	mux.HandleFunc("/ws/agent", srv.HandleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/traefik/config", traefikConfigHandler(database))
	mux.HandleFunc("/resources", createResourceHandler(database))

	log.Printf("control plane listening on %s (mesh cidr %s)", listenAddr, meshCIDR)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

// traefikConfigHandler serves Traefik's dynamic config JSON -- this is
// what Traefik's `http` provider polls (traefik.yml:
// `providers.http.endpoint`).
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

// createResourceRequest is the POST /resources body. No auth/dashboard
// yet for this milestone -- deliberately minimal, matching milestone
// 1's own no-auth scope for the same reason (a real auth layer is
// separate future work, not blocking mesh/Traefik-config proof).
// Targets is a list rather than a single target: http resources can
// have any number (a load-balanced pool), tcp resources at most a
// primary and a backup (validated in db.CreateResource).
type createResourceRequest struct {
	Name       string           `json:"name"`
	Protocol   string           `json:"protocol"`
	Domain     string           `json:"domain,omitempty"`
	EntryPoint string           `json:"entry_point"`
	Targets    []targetSpecJSON `json:"targets"`
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
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req createResourceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body: "+err.Error(), http.StatusBadRequest)
			return
		}

		var domain *string
		if req.Domain != "" {
			domain = &req.Domain
		}

		targets := make([]db.TargetSpec, len(req.Targets))
		for i, t := range req.Targets {
			targets[i] = db.TargetSpec{NodeName: t.NodeName, Address: t.Address, Port: t.Port, Role: t.Role}
		}

		err := database.CreateResource(r.Context(), req.Name, req.Protocol, domain, req.EntryPoint, targets)
		if err != nil {
			log.Printf("create resource %q: %v", req.Name, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusCreated)
	}
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
