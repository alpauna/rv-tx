package traefikconfig

import (
	"testing"

	"rv-tx/internal/controlplane/db"
)

func strPtr(s string) *string { return &s }

func TestGenerate_HTTP(t *testing.T) {
	cfg := Generate([]db.ResourceWithNode{
		{
			Name: "app", Protocol: "http", Domain: strPtr("app.example.com"),
			TargetMeshIP: "100.100.0.1", TargetPort: 8000, EntryPoint: "web",
		},
	})

	if cfg.HTTP == nil {
		t.Fatal("expected HTTP config, got nil")
	}
	if cfg.TCP != nil {
		t.Fatal("expected no TCP config for an http-only resource")
	}
	r, ok := cfg.HTTP.Routers["app"]
	if !ok {
		t.Fatal("expected router \"app\"")
	}
	if r.Rule != "Host(`app.example.com`)" {
		t.Errorf("got rule %q", r.Rule)
	}
	if r.Service != "app" || r.EntryPoints[0] != "web" {
		t.Errorf("got router %+v", r)
	}
	svc, ok := cfg.HTTP.Services["app"]
	if !ok {
		t.Fatal("expected service \"app\"")
	}
	if svc.LoadBalancer.Servers[0].URL != "http://100.100.0.1:8000" {
		t.Errorf("got server %+v", svc.LoadBalancer.Servers[0])
	}
}

func TestGenerate_TCPCatchAll(t *testing.T) {
	cfg := Generate([]db.ResourceWithNode{
		{
			Name: "raw", Protocol: "tcp", Domain: nil,
			TargetMeshIP: "100.100.0.2", TargetPort: 9000, EntryPoint: "raw-tcp",
		},
	})

	if cfg.TCP == nil {
		t.Fatal("expected TCP config, got nil")
	}
	r := cfg.TCP.Routers["raw"]
	if r.Rule != "HostSNI(`*`)" {
		t.Errorf("got rule %q, want catch-all", r.Rule)
	}
	svc := cfg.TCP.Services["raw"]
	if svc.LoadBalancer.Servers[0].Address != "100.100.0.2:9000" {
		t.Errorf("got server %+v", svc.LoadBalancer.Servers[0])
	}
	if svc.LoadBalancer.Servers[0].URL != "" {
		t.Errorf("TCP server should not set URL, got %q", svc.LoadBalancer.Servers[0].URL)
	}
}

func TestGenerate_Empty(t *testing.T) {
	cfg := Generate(nil)
	if cfg.HTTP != nil || cfg.TCP != nil {
		t.Errorf("expected both nil for no resources, got %+v", cfg)
	}
}
