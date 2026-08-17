package db

import (
	"context"
	"fmt"
	"time"
)

// TargetSpec is one requested backend target for a resource being
// created -- exactly one of NodeName (resolved to a node id inside
// CreateResource) or Address (used as-is, for a target that isn't an
// rv-tx mesh member -- e.g. a raw LAN IP already reachable from
// wherever Traefik runs) must be set.
type TargetSpec struct {
	NodeName string
	Address  string
	Port     int
	Role     string // "primary" or "backup"; "" defaults to "primary"
}

// Target is one resource's backend as returned for Traefik config
// generation -- carries the resolved address (a node's live mesh_ip,
// or the stored external address) and last_seen (for tcp/udp
// primary/backup liveness selection) rather than a value that could go
// stale if the node ever re-registers. External is true for a target
// with no rv-tx mesh node at all -- LastSeen is always nil for those,
// since there's no heartbeat to key off.
type Target struct {
	Address  string     `json:"address"`
	Port     int        `json:"port"`
	Role     string     `json:"role"`
	External bool       `json:"external"`
	LastSeen *time.Time `json:"last_seen"`
}

// ResourceWithTargets is a resource joined with all of its backend
// targets.
type ResourceWithTargets struct {
	Name             string   `json:"name"`
	Protocol         string   `json:"protocol"`
	Domain           *string  `json:"domain"`
	EntryPoint       string   `json:"entry_point"`
	CertResolver     *string  `json:"cert_resolver"`
	TargetScheme     string   `json:"target_scheme"`      // "http" or "https" -- http protocol only, ignored for tcp/udp
	TargetSkipVerify bool     `json:"target_skip_verify"` // skip TLS verification when TargetScheme is "https" (self-signed backend certs, e.g. Proxmox's own web UI)
	Targets          []Target `json:"targets"`
}

// CreateResource validates the target list for the given protocol,
// resolves each node-backed target by name, then inserts the resource
// and its targets in a single transaction. Validation is deliberately
// done in Go rather than DB constraints, matching this project's
// existing style (see UpsertNode's comments) -- http resources are an
// unbounded pool (any number of targets, role ignored), tcp/udp
// resources are at most a primary and a backup (not full load
// balancing yet).
func (db *DB) CreateResource(ctx context.Context, name, protocol string, domain *string, entryPoint string, certResolver *string, targetScheme string, targetSkipVerify bool, targets []TargetSpec) error {
	if err := validateTargets(protocol, targets); err != nil {
		return err
	}
	if targetScheme == "" {
		targetScheme = "http"
	}
	if targetScheme != "http" && targetScheme != "https" {
		return fmt.Errorf("target_scheme must be \"http\" or \"https\", got %q", targetScheme)
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create resource: %w", err)
	}
	defer tx.Rollback(ctx)

	var resourceID string
	err = tx.QueryRow(ctx, `
		INSERT INTO resources (name, protocol, domain, entry_point, cert_resolver, target_scheme, target_skip_verify)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id::text
	`, name, protocol, domain, entryPoint, certResolver, targetScheme, targetSkipVerify).Scan(&resourceID)
	if err != nil {
		return fmt.Errorf("insert resource: %w", err)
	}

	for _, t := range targets {
		role := t.Role
		if role == "" {
			role = "primary"
		}

		if t.Address != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO resource_targets (resource_id, node_id, address, port, role)
				VALUES ($1, NULL, $2, $3, $4)
			`, resourceID, t.Address, t.Port, role); err != nil {
				return fmt.Errorf("insert external target %q: %w", t.Address, err)
			}
			continue
		}

		var nodeID string
		err := tx.QueryRow(ctx,
			`SELECT id::text FROM nodes WHERE name = $1`, t.NodeName,
		).Scan(&nodeID)
		if err != nil {
			return fmt.Errorf("target node %q not found: %w", t.NodeName, err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO resource_targets (resource_id, node_id, address, port, role)
			VALUES ($1, $2, NULL, $3, $4)
		`, resourceID, nodeID, t.Port, role); err != nil {
			return fmt.Errorf("insert target %q: %w", t.NodeName, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create resource: %w", err)
	}
	return nil
}

// DeleteResource removes a resource by name. resource_targets rows
// cascade via the existing ON DELETE CASCADE foreign key -- no
// separate cleanup needed.
func (db *DB) DeleteResource(ctx context.Context, name string) error {
	tag, err := db.Pool.Exec(ctx, `DELETE FROM resources WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("delete resource %q: %w", name, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("resource %q not found", name)
	}
	return nil
}

func validateTargets(protocol string, targets []TargetSpec) error {
	if len(targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}

	for _, t := range targets {
		if (t.NodeName == "") == (t.Address == "") {
			return fmt.Errorf("each target needs exactly one of node_name or address")
		}
	}

	switch protocol {
	case "http":
		return nil

	case "tcp", "udp":
		if len(targets) > 2 {
			return fmt.Errorf("%s resources support at most 2 targets (primary + backup), got %d", protocol, len(targets))
		}
		if len(targets) == 2 {
			roles := map[string]bool{}
			for _, t := range targets {
				role := t.Role
				if role == "" {
					role = "primary"
				}
				roles[role] = true
			}
			if !roles["primary"] || !roles["backup"] {
				return fmt.Errorf("%s resources with 2 targets require exactly one \"primary\" and one \"backup\" role", protocol)
			}
		}
		return nil

	default:
		return fmt.Errorf("unknown protocol %q", protocol)
	}
}

// ListResourcesWithTargets returns every resource joined with all of
// its backend targets' current address/last_seen, for Traefik config
// generation. Node-backed targets resolve to the node's live mesh_ip;
// external targets use their stored address directly and never have a
// last_seen (no heartbeat to key off).
func (db *DB) ListResourcesWithTargets(ctx context.Context) ([]ResourceWithTargets, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT r.id::text, r.name, r.protocol, r.domain, r.entry_point, r.cert_resolver,
		       r.target_scheme, r.target_skip_verify,
		       COALESCE(host(n.mesh_ip), rt.address), rt.port, rt.role,
		       (rt.node_id IS NULL), n.last_seen
		FROM resources r
		JOIN resource_targets rt ON rt.resource_id = r.id
		LEFT JOIN nodes n ON n.id = rt.node_id
		ORDER BY r.name, rt.role
	`)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	defer rows.Close()

	byID := make(map[string]*ResourceWithTargets)
	var order []string

	for rows.Next() {
		var (
			id, name, protocol, entryPoint, address, role, targetScheme string
			domain, certResolver                                        *string
			port                                                        int
			targetSkipVerify, external                                  bool
			lastSeen                                                    *time.Time
		)
		if err := rows.Scan(&id, &name, &protocol, &domain, &entryPoint, &certResolver, &targetScheme, &targetSkipVerify, &address, &port, &role, &external, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan resource target: %w", err)
		}

		r, ok := byID[id]
		if !ok {
			r = &ResourceWithTargets{Name: name, Protocol: protocol, Domain: domain, EntryPoint: entryPoint, CertResolver: certResolver, TargetScheme: targetScheme, TargetSkipVerify: targetSkipVerify}
			byID[id] = r
			order = append(order, id)
		}
		r.Targets = append(r.Targets, Target{Address: address, Port: port, Role: role, External: external, LastSeen: lastSeen})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]ResourceWithTargets, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}
