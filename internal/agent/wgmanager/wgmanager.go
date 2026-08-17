// Package wgmanager reconciles a local WireGuard interface to match a
// desired peer list. Shells out to `wg`/`ip` rather than a pure-Go
// WireGuard implementation -- deliberate choice for this milestone: both
// tools are already proven working on every target VM in this project,
// and it's far less code to get right than reimplementing peer
// management against wireguard-go/netlink directly.
package wgmanager

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"rv-tx/internal/protocol"
)

type Manager struct {
	Interface string
}

func New(iface string) *Manager {
	return &Manager{Interface: iface}
}

// GenerateKeypair returns (privateKey, publicKey) via `wg genkey`/`wg pubkey`.
func GenerateKeypair() (string, string, error) {
	privOut, err := exec.Command("wg", "genkey").Output()
	if err != nil {
		return "", "", fmt.Errorf("wg genkey: %w", err)
	}
	priv := strings.TrimSpace(string(privOut))

	pubCmd := exec.Command("wg", "pubkey")
	pubCmd.Stdin = strings.NewReader(priv)
	pubOut, err := pubCmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("wg pubkey: %w", err)
	}
	pub := strings.TrimSpace(string(pubOut))

	return priv, pub, nil
}

// PubKeyFor derives the public key for an existing private key via
// `wg pubkey`, for loading a previously-persisted identity on restart
// (only the private key is saved to disk; the public key is always
// re-derived, never stored separately, so there's no chance of the two
// drifting apart).
func PubKeyFor(privateKey string) (string, error) {
	cmd := exec.Command("wg", "pubkey")
	cmd.Stdin = strings.NewReader(privateKey)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("wg pubkey: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// EnsureInterface creates the WireGuard interface if it doesn't already
// exist, assigns it meshIP, sets its private key, and brings it up.
// Idempotent -- safe to call on every agent startup.
func (m *Manager) EnsureInterface(privateKey, meshIP string, listenPort int) error {
	if !m.interfaceExists() {
		if err := run("ip", "link", "add", "dev", m.Interface, "type", "wireguard"); err != nil {
			return fmt.Errorf("create interface: %w", err)
		}
	}

	if err := runWithStdin(privateKey, "wg", "set", m.Interface,
		"private-key", "/dev/stdin",
		"listen-port", strconv.Itoa(listenPort),
	); err != nil {
		return fmt.Errorf("set private key: %w", err)
	}

	// Check first rather than pattern-matching "already exists" error
	// text -- iproute2 phrases that error differently across versions
	// ("File exists" vs "Address already assigned", both observed during
	// testing), too fragile to match reliably. Skipping the add when the
	// address is already correct also sidesteps that entirely.
	hasAddr, err := m.hasAddress(meshIP)
	if err != nil {
		return fmt.Errorf("check existing address: %w", err)
	}
	if !hasAddr {
		// Retry once after a brief delay: a freshly-created netlink
		// device can transiently fail an immediate `ip addr add`
		// (observed directly during testing -- the same command
		// succeeds run by hand moments later against the same
		// interface).
		if err := run("ip", "addr", "add", meshIP+"/32", "dev", m.Interface); err != nil {
			time.Sleep(200 * time.Millisecond)
			if retryErr := run("ip", "addr", "add", meshIP+"/32", "dev", m.Interface); retryErr != nil {
				return fmt.Errorf("assign address: %w", retryErr)
			}
		}
	}

	if err := run("ip", "link", "set", "up", "dev", m.Interface); err != nil {
		return fmt.Errorf("bring interface up: %w", err)
	}
	return nil
}

func (m *Manager) interfaceExists() bool {
	return exec.Command("ip", "link", "show", m.Interface).Run() == nil
}

// hasAddress reports whether meshIP is already assigned to the
// interface, via `ip -4 addr show`'s machine-parseable-enough output
// rather than trying to match ip's error text.
func (m *Manager) hasAddress(meshIP string) (bool, error) {
	out, err := exec.Command("ip", "-4", "addr", "show", "dev", m.Interface).Output()
	if err != nil {
		return false, fmt.Errorf("ip addr show: %w", err)
	}
	return strings.Contains(string(out), meshIP+"/32"), nil
}

// Reconcile diffs the interface's current peers against the desired
// list and adds/updates/removes to match exactly. Also adds/removes the
// kernel routing table entry for each peer's mesh IP: `wg set` alone
// only configures WireGuard's own crypto-key routing (which peer to
// encrypt an outbound packet for once it's already headed out this
// interface) -- it does NOT touch the kernel's regular routing table, so
// without an explicit `ip route`, the kernel never sends traffic to a
// peer's mesh IP out this interface in the first place. `wg-quick`
// normally does this automatically by parsing AllowedIPs from its config
// file; managing the interface directly via `wg`/`ip` means doing it
// ourselves. Confirmed missing was the actual cause of a real "zero
// packets leave the host at all" failure during testing, not just a
// theoretical concern.
func (m *Manager) Reconcile(desired []protocol.PeerInfo) error {
	current, err := m.currentPeers()
	if err != nil {
		return fmt.Errorf("read current peers: %w", err)
	}

	desiredByKey := make(map[string]protocol.PeerInfo, len(desired))
	for _, p := range desired {
		desiredByKey[p.PublicKey] = p
	}

	for pubKey, allowedIP := range current {
		if _, want := desiredByKey[pubKey]; !want {
			if err := run("wg", "set", m.Interface, "peer", pubKey, "remove"); err != nil {
				return fmt.Errorf("remove peer %s: %w", pubKey, err)
			}
			// Best-effort: route may already be gone (e.g. interface
			// recreated), don't fail reconciliation over cleanup.
			if allowedIP != "" {
				_ = run("ip", "route", "del", allowedIP, "dev", m.Interface)
			}
		}
	}

	for _, p := range desired {
		args := []string{"set", m.Interface, "peer", p.PublicKey,
			"allowed-ips", p.MeshIP + "/32"}
		if p.Endpoint != "" {
			args = append(args, "endpoint", p.Endpoint)
		}
		if err := run("wg", args...); err != nil {
			return fmt.Errorf("set peer %s: %w", p.PublicKey, err)
		}

		hasRoute, err := m.hasRoute(p.MeshIP)
		if err != nil {
			return fmt.Errorf("check route for %s: %w", p.MeshIP, err)
		}
		if !hasRoute {
			if err := run("ip", "route", "add", p.MeshIP+"/32", "dev", m.Interface); err != nil {
				return fmt.Errorf("add route for %s: %w", p.MeshIP, err)
			}
		}
	}
	return nil
}

func (m *Manager) hasRoute(meshIP string) (bool, error) {
	out, err := exec.Command("ip", "route", "show", meshIP+"/32", "dev", m.Interface).Output()
	if err != nil {
		return false, fmt.Errorf("ip route show: %w", err)
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// currentPeers returns the public keys currently configured on the
// interface, mapped to their current allowed-ips (needed to clean up
// routes for peers being removed), via `wg show <iface> allowed-ips`.
func (m *Manager) currentPeers() (map[string]string, error) {
	out, err := exec.Command("wg", "show", m.Interface, "allowed-ips").Output()
	if err != nil {
		// A fresh interface with no peers yet exits non-zero on some wg
		// versions with empty output -- treat that as "no peers", not
		// an error, rather than failing reconciliation on first run.
		if len(out) == 0 {
			return map[string]string{}, nil
		}
		return nil, err
	}
	peers := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 1 {
			allowedIP := ""
			if len(fields) >= 2 {
				allowedIP = fields[1]
			}
			peers[fields[0]] = allowedIP
		}
	}
	return peers, nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runWithStdin(stdin string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
