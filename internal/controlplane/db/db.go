// Package db owns Postgres persistence for the control plane: connecting,
// and the raw queries used by the mesh/wsserver packages. Allocation
// algorithms and peer-list business logic live in the mesh package, not
// here -- this package is persistence only.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

func Connect(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{Pool: pool}, nil
}

func (db *DB) Close() {
	db.Pool.Close()
}

// Node mirrors the nodes table.
type Node struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	PublicKey         string     `json:"public_key"`
	MeshIP            string     `json:"mesh_ip"`
	LastEndpoint      *string    `json:"last_endpoint"`
	LastSeen          *time.Time `json:"last_seen"`
	AdvertisedSubnets []string   `json:"advertised_subnets"`
}

// UsedMeshIPs returns every mesh_ip currently allocated, for the mesh
// package's allocator to avoid colliding with.
func (db *DB) UsedMeshIPs(ctx context.Context) ([]string, error) {
	rows, err := db.Pool.Query(ctx, `SELECT host(mesh_ip) FROM nodes`)
	if err != nil {
		return nil, fmt.Errorf("query used mesh ips: %w", err)
	}
	defer rows.Close()

	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("scan mesh ip: %w", err)
		}
		ips = append(ips, ip)
	}
	return ips, rows.Err()
}

// UpsertNode registers a node by (name, public_key) unique keys: if a
// node with this name already exists, its public_key/mesh_ip/endpoint
// are updated in place (handles an agent re-registering, e.g. after a
// reinstall or reconnect from a new network) rather than allocating a
// second identity for the same operator-chosen name. endpoint is set
// atomically here rather than waiting for a separate heartbeat, since
// it's already known (auto-detected or overridden) by the time
// register is handled -- closes a small gap where a freshly registered
// node previously had no endpoint until its first heartbeat landed.
// Returns the node's final mesh_ip.
func (db *DB) UpsertNode(ctx context.Context, name, publicKey, meshIP, endpoint string, advertisedSubnets []string) (string, error) {
	if advertisedSubnets == nil {
		advertisedSubnets = []string{}
	}
	var finalMeshIP string
	err := db.Pool.QueryRow(ctx, `
		INSERT INTO nodes (name, public_key, mesh_ip, last_endpoint, last_seen, advertised_subnets)
		VALUES ($1, $2, $3, $4, now(), $5)
		ON CONFLICT (name) DO UPDATE
			SET public_key = EXCLUDED.public_key,
			    last_endpoint = EXCLUDED.last_endpoint,
			    last_seen = now(),
			    advertised_subnets = EXCLUDED.advertised_subnets
		RETURNING host(mesh_ip)
	`, name, publicKey, meshIP, endpoint, advertisedSubnets).Scan(&finalMeshIP)
	if err != nil {
		return "", fmt.Errorf("upsert node: %w", err)
	}
	return finalMeshIP, nil
}

// TouchLastSeen refreshes a node's liveness timestamp on each
// heartbeat, keyed by public_key (what the agent proves ownership of
// on every message this milestone -- no separate auth token yet).
// Endpoint is not touched here -- it's fixed at register time.
func (db *DB) TouchLastSeen(ctx context.Context, publicKey string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE nodes SET last_seen = now()
		WHERE public_key = $1
	`, publicKey)
	if err != nil {
		return fmt.Errorf("touch last seen: %w", err)
	}
	return nil
}

// ListNodes returns every registered node, for peer-list computation.
func (db *DB) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id::text, name, public_key, host(mesh_ip), last_endpoint, last_seen, advertised_subnets
		FROM nodes
	`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Name, &n.PublicKey, &n.MeshIP, &n.LastEndpoint, &n.LastSeen, &n.AdvertisedSubnets); err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}
