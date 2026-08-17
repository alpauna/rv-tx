package db

import (
	"context"
	"fmt"
)

// ResourceWithNode is a resource joined with its target node's current
// mesh_ip -- generated Traefik config always needs the live address, not
// a value that could go stale if the node ever re-registers.
type ResourceWithNode struct {
	Name         string
	Protocol     string
	Domain       *string
	TargetMeshIP string
	TargetPort   int
	EntryPoint   string
}

// CreateResource looks up targetNodeName to get its node id, then
// inserts the resource. Returns a descriptive error if the named node
// doesn't exist rather than a raw FK-violation from Postgres.
func (db *DB) CreateResource(ctx context.Context, name, protocol string, domain *string, targetNodeName string, targetPort int, entryPoint string) error {
	var targetNodeID string
	err := db.Pool.QueryRow(ctx,
		`SELECT id::text FROM nodes WHERE name = $1`, targetNodeName,
	).Scan(&targetNodeID)
	if err != nil {
		return fmt.Errorf("target node %q not found: %w", targetNodeName, err)
	}

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO resources (name, protocol, domain, target_node_id, target_port, entry_point)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, name, protocol, domain, targetNodeID, targetPort, entryPoint)
	if err != nil {
		return fmt.Errorf("insert resource: %w", err)
	}
	return nil
}

// ListResourcesWithNodes returns every resource joined with its target
// node's current mesh_ip, for Traefik config generation.
func (db *DB) ListResourcesWithNodes(ctx context.Context) ([]ResourceWithNode, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT r.name, r.protocol, r.domain, host(n.mesh_ip), r.target_port, r.entry_point
		FROM resources r
		JOIN nodes n ON n.id = r.target_node_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	defer rows.Close()

	var out []ResourceWithNode
	for rows.Next() {
		var r ResourceWithNode
		if err := rows.Scan(&r.Name, &r.Protocol, &r.Domain, &r.TargetMeshIP, &r.TargetPort, &r.EntryPoint); err != nil {
			return nil, fmt.Errorf("scan resource: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
