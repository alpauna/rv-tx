// Command controlplane runs the rv-tx control plane: Postgres-backed
// mesh coordination over WebSocket. Config entirely from env vars,
// matching the .env convention already used elsewhere in this project.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"rv-tx/internal/controlplane/db"
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

	log.Printf("control plane listening on %s (mesh cidr %s)", listenAddr, meshCIDR)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("http server: %v", err)
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
