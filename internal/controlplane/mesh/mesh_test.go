package mesh

import "testing"

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
