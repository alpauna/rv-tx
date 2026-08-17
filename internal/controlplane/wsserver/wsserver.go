// Package wsserver is the control plane's WebSocket endpoint: accepts
// agent connections, handles register/heartbeat, and broadcasts an
// updated peer_list to every connected agent whenever the node set
// changes. Known simplification for this milestone: peer_list is always
// computed from every row in the nodes table regardless of current
// connection status -- a disconnected peer just won't answer WireGuard
// handshakes, which is fine at this layer; no separate liveness/pruning
// logic yet.
package wsserver

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"rv-tx/internal/controlplane/db"
	"rv-tx/internal/controlplane/mesh"
	"rv-tx/internal/protocol"
)

type Server struct {
	DB       *db.DB
	MeshCIDR string

	upgrader websocket.Upgrader

	mu    sync.Mutex
	conns map[string]*websocket.Conn // public_key -> connection
}

func New(database *db.DB, meshCIDR string) *Server {
	return &Server{
		DB:       database,
		MeshCIDR: meshCIDR,
		upgrader: websocket.Upgrader{
			// Agents are trusted infra nodes connecting directly by IP,
			// not browsers -- no CSRF-style origin concern here.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		conns: make(map[string]*websocket.Conn),
	}
}

func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	ctx := r.Context()
	var publicKey string

	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr is always host:port for a real TCP connection;
		// this would only trip on a malformed/unexpected transport.
		log.Printf("could not parse remote addr %q: %v", r.RemoteAddr, err)
		remoteHost = r.RemoteAddr
	}

	for {
		var env protocol.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			log.Printf("ws read closed (public_key=%q): %v", publicKey, err)
			break
		}

		switch env.Type {
		case protocol.TypeRegister:
			var p protocol.RegisterPayload
			if err := protocol.DecodePayload(env, &p); err != nil {
				log.Printf("bad register payload: %v", err)
				continue
			}
			publicKey = p.PublicKey
			if err := s.handleRegister(ctx, conn, remoteHost, p); err != nil {
				log.Printf("register failed for %q: %v", p.Name, err)
				continue
			}

		case protocol.TypeHeartbeat:
			if publicKey == "" {
				log.Printf("heartbeat before register, dropping")
				continue
			}
			if err := s.handleHeartbeat(ctx, publicKey); err != nil {
				log.Printf("heartbeat failed for %q: %v", publicKey, err)
			}

		default:
			log.Printf("unknown message type: %s", env.Type)
		}
	}

	if publicKey != "" {
		s.mu.Lock()
		delete(s.conns, publicKey)
		s.mu.Unlock()
	}
}

func (s *Server) handleRegister(ctx context.Context, conn *websocket.Conn, remoteHost string, p protocol.RegisterPayload) error {
	used, err := s.DB.UsedMeshIPs(ctx)
	if err != nil {
		return err
	}
	candidateIP, err := mesh.AllocateIP(s.MeshCIDR, used)
	if err != nil {
		return err
	}

	var endpoint string
	if p.Endpoint != "" {
		endpoint = p.Endpoint
		log.Printf("endpoint override supplied for %q: %s", p.Name, endpoint)
	} else {
		endpoint = fmt.Sprintf("%s:%d", remoteHost, p.Port)
		log.Printf("endpoint auto-detected from connection for %q: %s", p.Name, endpoint)
	}

	// UpsertNode is keyed on name: if this node already has an assigned
	// mesh_ip from a prior registration, that existing IP wins over
	// candidateIP (ON CONFLICT DO UPDATE only touches public_key/
	// last_endpoint/last_seen, not mesh_ip) -- candidateIP is only
	// actually used for brand new nodes.
	meshIP, err := s.DB.UpsertNode(ctx, p.Name, p.PublicKey, candidateIP, endpoint)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.conns[p.PublicKey] = conn
	s.mu.Unlock()

	nodes, err := s.DB.ListNodes(ctx)
	if err != nil {
		return err
	}

	if err := conn.WriteJSON(protocol.Envelope{
		Type: protocol.TypeRegistered,
		Payload: protocol.RegisteredPayload{
			MeshIP: meshIP,
			Peers:  mesh.PeerListFor(p.PublicKey, nodes),
		},
	}); err != nil {
		return err
	}

	s.broadcastPeerLists(nodes)
	return nil
}

func (s *Server) handleHeartbeat(ctx context.Context, publicKey string) error {
	if err := s.DB.TouchLastSeen(ctx, publicKey); err != nil {
		return err
	}
	nodes, err := s.DB.ListNodes(ctx)
	if err != nil {
		return err
	}
	s.broadcastPeerLists(nodes)
	return nil
}

// broadcastPeerLists sends every currently-connected agent its own
// personalized peer_list (everyone except itself).
func (s *Server) broadcastPeerLists(nodes []db.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for publicKey, conn := range s.conns {
		env := protocol.Envelope{
			Type: protocol.TypePeerList,
			Payload: protocol.PeerListPayload{
				Peers: mesh.PeerListFor(publicKey, nodes),
			},
		}
		if err := conn.WriteJSON(env); err != nil {
			log.Printf("broadcast to %q failed: %v", publicKey, err)
		}
	}
}
