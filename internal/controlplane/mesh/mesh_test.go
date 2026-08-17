package mesh

import (
	"testing"

	"rv-tx/internal/controlplane/db"
)

func TestAllocateIP_First(t *testing.T) {
	ip, err := AllocateIP("100.100.0.0/30", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// .0 is the network address, so the first allocatable address is .1.
	if ip != "100.100.0.1" {
		t.Errorf("got %q, want 100.100.0.1", ip)
	}
}

func TestAllocateIP_SkipsUsed(t *testing.T) {
	ip, err := AllocateIP("100.100.0.0/30", []string{"100.100.0.1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != "100.100.0.2" {
		t.Errorf("got %q, want 100.100.0.2", ip)
	}
}

func TestAllocateIP_Exhausted(t *testing.T) {
	// /30 has 4 addresses: .0 (network), .1, .2, .3 (broadcast). Only .1
	// and .2 are allocatable; both used means exhaustion.
	_, err := AllocateIP("100.100.0.0/30", []string{"100.100.0.1", "100.100.0.2"})
	if err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
}

func TestAllocateIP_InvalidCIDR(t *testing.T) {
	_, err := AllocateIP("not-a-cidr", nil)
	if err == nil {
		t.Fatal("expected error for invalid CIDR, got nil")
	}
}

func TestPeerListFor_CarriesAdvertisedSubnets(t *testing.T) {
	nodes := []db.Node{
		{PublicKey: "self-key", MeshIP: "100.100.0.1"},
		{PublicKey: "relay-key", MeshIP: "100.100.0.2", AdvertisedSubnets: []string{"192.168.7.0/24"}},
		{PublicKey: "plain-key", MeshIP: "100.100.0.3"},
	}

	peers := PeerListFor("self-key", nodes)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers (excluding self), got %d", len(peers))
	}

	byKey := map[string][]string{}
	for _, p := range peers {
		byKey[p.PublicKey] = p.AdvertisedSubnets
	}

	if got := byKey["relay-key"]; len(got) != 1 || got[0] != "192.168.7.0/24" {
		t.Errorf("expected relay-key to carry [192.168.7.0/24], got %v", got)
	}
	if got := byKey["plain-key"]; len(got) != 0 {
		t.Errorf("expected plain-key to carry no subnets, got %v", got)
	}
}
