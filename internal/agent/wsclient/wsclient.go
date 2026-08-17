// Package wsclient is the agent's WebSocket connection to the control
// plane: register on connect, apply the initial peer list, then keep
// reconciling on every peer_list push while sending periodic heartbeats.
// Reconnect/backoff pattern matches NetMonitor's own agents and
// Pangolin's newt client, both proven in this project already.
package wsclient

import (
	"context"
	"log"
	"time"

	"github.com/gorilla/websocket"

	"rv-tx/internal/agent/wgmanager"
	"rv-tx/internal/protocol"
)

type Client struct {
	URL               string
	Name              string
	PrivateKey        string
	PublicKey         string
	ListenPort        int
	HeartbeatInterval time.Duration
	// EndpointOverride is optional -- leave empty to let the control
	// plane auto-detect the endpoint from the connection's source IP
	// plus ListenPort. Only set this for NAT cases auto-detection can't
	// handle (asymmetric/symmetric NAT where the outbound UDP mapping
	// differs from the TCP connection's source port).
	EndpointOverride string
	WG               *wgmanager.Manager
}

// Run connects and reconnects forever with exponential backoff (capped),
// until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	backoff := 2 * time.Second
	const maxBackoff = 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := c.connectOnce(ctx); err != nil {
			log.Printf("connection lost: %v, retrying in %s", err, backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) connectOnce(ctx context.Context) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.URL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	log.Printf("connected to control plane at %s", c.URL)

	if err := conn.WriteJSON(protocol.Envelope{
		Type: protocol.TypeRegister,
		Payload: protocol.RegisterPayload{
			Name:      c.Name,
			PublicKey: c.PublicKey,
			Port:      c.ListenPort,
			Endpoint:  c.EndpointOverride,
		},
	}); err != nil {
		return err
	}

	hbCtx, cancelHB := context.WithCancel(ctx)
	defer cancelHB()
	go c.heartbeatLoop(hbCtx, conn)

	for {
		var env protocol.Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}

		switch env.Type {
		case protocol.TypeRegistered:
			var p protocol.RegisteredPayload
			if err := protocol.DecodePayload(env, &p); err != nil {
				log.Printf("bad registered payload: %v", err)
				continue
			}
			log.Printf("registered, assigned mesh ip %s, %d initial peers", p.MeshIP, len(p.Peers))
			if err := c.applyPeerList(p.MeshIP, p.Peers); err != nil {
				log.Printf("apply initial peer list failed: %v", err)
			}

		case protocol.TypePeerList:
			var p protocol.PeerListPayload
			if err := protocol.DecodePayload(env, &p); err != nil {
				log.Printf("bad peer_list payload: %v", err)
				continue
			}
			log.Printf("peer_list update, %d peers", len(p.Peers))
			if err := c.WG.Reconcile(p.Peers); err != nil {
				log.Printf("reconcile failed: %v", err)
			}

		default:
			log.Printf("unknown message type: %s", env.Type)
		}
	}
}

// applyPeerList handles the one-time interface setup on first
// registration -- mesh_ip is only known once the control plane assigns
// it, so the interface can't be fully configured until this point.
// EnsureInterface is idempotent, safe to call again on a reconnect.
func (c *Client) applyPeerList(meshIP string, peers []protocol.PeerInfo) error {
	if err := c.WG.EnsureInterface(c.PrivateKey, meshIP, c.ListenPort); err != nil {
		return err
	}
	return c.WG.Reconcile(peers)
}

func (c *Client) heartbeatLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(c.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := conn.WriteJSON(protocol.Envelope{
				Type:    protocol.TypeHeartbeat,
				Payload: protocol.HeartbeatPayload{},
			})
			if err != nil {
				log.Printf("heartbeat send failed: %v", err)
				return
			}
		}
	}
}
