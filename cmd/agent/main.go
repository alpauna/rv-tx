// Command agent is the rv-tx mesh node agent: connects to the control
// plane over WebSocket, registers, and reconciles a local WireGuard
// interface to match the pushed peer list. Config entirely from env
// vars, matching the .env convention already used elsewhere in this
// project.
package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"rv-tx/internal/agent/wgmanager"
	"rv-tx/internal/agent/wsclient"
)

func main() {
	controlPlaneURL := requireEnv("RVTX_CONTROLPLANE_URL")
	name := requireEnv("RVTX_NODE_NAME")
	// Optional -- leave unset to let the control plane auto-detect the
	// endpoint from the connection's source IP + RVTX_LISTEN_PORT. Only
	// needed for NAT cases auto-detection can't handle.
	endpointOverride := envOr("RVTX_ENDPOINT_OVERRIDE", "")
	iface := envOr("RVTX_INTERFACE", "rvtx0")
	keyPath := envOr("RVTX_KEY_PATH", "/etc/rvtx/private.key")
	listenPort := envOrInt("RVTX_LISTEN_PORT", 51820)
	heartbeatSeconds := envOrInt("RVTX_HEARTBEAT_SECONDS", 15)

	privateKey, publicKey, err := loadOrCreateKeypair(keyPath)
	if err != nil {
		log.Fatalf("load/create keypair: %v", err)
	}
	log.Printf("node %q public key: %s", name, publicKey)

	wg := wgmanager.New(iface)

	client := &wsclient.Client{
		URL:               controlPlaneURL,
		Name:              name,
		PrivateKey:        privateKey,
		PublicKey:         publicKey,
		ListenPort:        listenPort,
		HeartbeatInterval: time.Duration(heartbeatSeconds) * time.Second,
		EndpointOverride:  endpointOverride,
		WG:                wg,
	}

	client.Run(context.Background())
}

// loadOrCreateKeypair reads a persistent private key from path, or
// generates and saves a new one if none exists yet -- same reasoning as
// gerbil's --generateAndSaveKeyTo: a node's identity must survive
// restarts, or every reconnect looks like a brand new node to the
// control plane and every existing peer's session breaks.
func loadOrCreateKeypair(path string) (privateKey, publicKey string, err error) {
	if data, readErr := os.ReadFile(path); readErr == nil {
		priv := string(data)
		pub, pubErr := wgmanager.PubKeyFor(priv)
		if pubErr != nil {
			return "", "", pubErr
		}
		return priv, pub, nil
	}

	priv, pub, genErr := wgmanager.GenerateKeypair()
	if genErr != nil {
		return "", "", genErr
	}
	if mkErr := os.MkdirAll(dirOf(path), 0700); mkErr != nil {
		return "", "", mkErr
	}
	if writeErr := os.WriteFile(path, []byte(priv), 0600); writeErr != nil {
		return "", "", writeErr
	}
	return priv, pub, nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
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

func envOrInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("env var %s must be an integer, got %q", key, v)
	}
	return n
}
