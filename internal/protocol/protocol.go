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
type RegisterPayload struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// RegisteredPayload is the control plane's reply to Register: the mesh
// IP it assigned to this node, plus the initial full peer list.
type RegisteredPayload struct {
	MeshIP string     `json:"mesh_ip"`
	Peers  []PeerInfo `json:"peers"`
}

// HeartbeatPayload is sent periodically by an agent to report its
// current best-guess reachable endpoint.
type HeartbeatPayload struct {
	Endpoint string `json:"endpoint"`
}

// PeerListPayload carries the full current set of mesh peers (excluding
// the receiving node itself).
type PeerListPayload struct {
	Peers []PeerInfo `json:"peers"`
}

// PeerInfo describes one mesh node as seen by another node's WireGuard
// interface: enough to configure a peer entry.
type PeerInfo struct {
	PublicKey string `json:"public_key"`
	MeshIP    string `json:"mesh_ip"`
	Endpoint  string `json:"endpoint,omitempty"`
}
