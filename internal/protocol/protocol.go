// Package protocol defines the WebSocket message envelopes shared between
// the control plane and node agents. Same typed-envelope pattern used by
// NetMonitor's own agents and Pangolin's newt protocol: a message type
// string plus a type-specific payload.
package protocol

// MessageType identifies the kind of message in an Envelope.
type MessageType string

const (
	// Agent -> control plane, sent once on connect.
	TypeRegister MessageType = "register"
	// Control plane -> agent, reply to Register.
	TypeRegistered MessageType = "registered"
	// Agent -> control plane, sent periodically to report reachability.
	TypeHeartbeat MessageType = "heartbeat"
	// Control plane -> agent, sent whenever the peer set changes. Always
	// the full current peer list, not a diff.
	TypePeerList MessageType = "peer_list"
)

// Envelope is the outer shape of every WebSocket message in both
// directions. Payload is re-marshaled/unmarshaled based on Type.
type Envelope struct {
	Type    MessageType `json:"type"`
	Payload interface{} `json:"payload"`
}

// RegisterPayload is sent by an agent immediately after connecting.
// Port is the agent's local WireGuard listen port -- combined with the
// control plane's own view of the connection's source IP, this is
// enough to auto-detect a correct endpoint without operator config for
// the common case (LAN hosts, hosts with a routable IP and no
// unpredictable NAT port remapping). Endpoint is an optional manual
// override for cases that don't hold (asymmetric/symmetric NAT where
// the outbound UDP mapping differs from the TCP connection's source
// port) -- if set, it wins over auto-detection.
type RegisterPayload struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	Port      int    `json:"port"`
	Endpoint  string `json:"endpoint,omitempty"`
	// AdvertisedSubnets are extra CIDRs (beyond this node's own mesh
	// IP) that this node can relay traffic for -- e.g. a node sitting
	// on 192.168.7.0/24 advertising that whole subnet, via MASQUERADE
	// on its own host, so every other mesh peer can reach it through
	// the tunnel without needing their own routing to it. Empty for a
	// normal node.
	AdvertisedSubnets []string `json:"advertised_subnets,omitempty"`
}

// RegisteredPayload is the control plane's reply to Register: the mesh
// IP it assigned to this node, plus the initial full peer list.
type RegisteredPayload struct {
	MeshIP string     `json:"mesh_ip"`
	Peers  []PeerInfo `json:"peers"`
}

// HeartbeatPayload is sent periodically by an agent purely as a
// liveness ping -- endpoint is fixed at register time (see
// RegisterPayload), since every reconnect re-registers, so there's
// nothing else to carry here.
type HeartbeatPayload struct{}

// PeerListPayload carries the full current set of mesh peers (excluding
// the receiving node itself).
type PeerListPayload struct {
	Peers []PeerInfo `json:"peers"`
}

// PeerInfo describes one mesh node as seen by another node's WireGuard
// interface: enough to configure a peer entry.
type PeerInfo struct {
	PublicKey         string   `json:"public_key"`
	MeshIP            string   `json:"mesh_ip"`
	Endpoint          string   `json:"endpoint,omitempty"`
	AdvertisedSubnets []string `json:"advertised_subnets,omitempty"`
}
