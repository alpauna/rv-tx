// Package mesh contains the control plane's mesh business logic: IP
// allocation within the configured CIDR, and computing each node's view
// of the peer list. No persistence here -- that's the db package's job.
package mesh

import (
	"fmt"
	"net"

	"rv-tx/internal/controlplane/db"
	"rv-tx/internal/protocol"
)

// AllocateIP returns the first address inside cidr that isn't in used,
// skipping the network and broadcast addresses. Sequential allocation is
// intentionally simple for this milestone -- no reclamation of IPs from
// removed nodes beyond "not in used", no randomization.
func AllocateIP(cidr string, used []string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parse mesh cidr %q: %w", cidr, err)
	}

	usedSet := make(map[string]bool, len(used))
	for _, u := range used {
		usedSet[u] = true
	}

	ip := ipnet.IP.Mask(ipnet.Mask)
	broadcast := lastAddr(ipnet)

	// Skip the network address itself.
	incIP(ip)

	for ipnet.Contains(ip) && !ip.Equal(broadcast) {
		candidate := ip.String()
		if !usedSet[candidate] {
			return candidate, nil
		}
		incIP(ip)
	}
	return "", fmt.Errorf("mesh cidr %q exhausted", cidr)
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

func lastAddr(ipnet *net.IPNet) net.IP {
	ip := make(net.IP, len(ipnet.IP))
	for i := range ip {
		ip[i] = ipnet.IP[i] | ^ipnet.Mask[i]
	}
	return ip
}

// PeerListFor builds the peer list a given node (identified by
// publicKey) should see: every other registered node, excluding itself.
func PeerListFor(publicKey string, nodes []db.Node) []protocol.PeerInfo {
	peers := make([]protocol.PeerInfo, 0, len(nodes))
	for _, n := range nodes {
		if n.PublicKey == publicKey {
			continue
		}
		endpoint := ""
		if n.LastEndpoint != nil {
			endpoint = *n.LastEndpoint
		}
		peers = append(peers, protocol.PeerInfo{
			PublicKey: n.PublicKey,
			MeshIP:    n.MeshIP,
			Endpoint:  endpoint,
		})
	}
	return peers
}
