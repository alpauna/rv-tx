package traefikconfig

import (
	"testing"
	"time"

	"rv-tx/internal/controlplane/db"
)

func strPtr(s string) *string        { return &s }
func timePtr(t time.Time) *time.Time { return &t }

func TestGenerate_HTTP(t *testing.T) {
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "app", Protocol: "http", Domain: strPtr("app.example.com"), EntryPoint: "web",
			Targets: []db.Target{{MeshIP: "100.100.0.1", Port: 8000, Role: "primary"}},
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
	if svc.LoadBalancer.Sticky != nil || svc.LoadBalancer.HealthCheck != nil {
		t.Errorf("single-target resource should not get sticky/healthCheck, got %+v", svc.LoadBalancer)
	}
}

func TestGenerate_HTTPMultiTargetSticky(t *testing.T) {
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "proxmox-ui", Protocol: "http", Domain: strPtr("pve.example.com"), EntryPoint: "web",
			Targets: []db.Target{
				{MeshIP: "100.100.0.1", Port: 8006, Role: "primary"},
				{MeshIP: "100.100.0.2", Port: 8006, Role: "primary"},
				{MeshIP: "100.100.0.3", Port: 8006, Role: "primary"},
			},
		},
	})

	svc := cfg.HTTP.Services["proxmox-ui"]
	if len(svc.LoadBalancer.Servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(svc.LoadBalancer.Servers))
	}
	if svc.LoadBalancer.Sticky == nil || svc.LoadBalancer.Sticky.Cookie.Name != "rvtx_lb_proxmox-ui" {
		t.Errorf("expected sticky cookie \"rvtx_lb_proxmox-ui\", got %+v", svc.LoadBalancer.Sticky)
	}
	if svc.LoadBalancer.HealthCheck == nil || svc.LoadBalancer.HealthCheck.Path != "/" {
		t.Errorf("expected healthCheck with path \"/\", got %+v", svc.LoadBalancer.HealthCheck)
	}
}

func TestGenerate_TCPCatchAll(t *testing.T) {
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "raw", Protocol: "tcp", Domain: nil, EntryPoint: "raw-tcp",
			Targets: []db.Target{{MeshIP: "100.100.0.2", Port: 9000, Role: "primary"}},
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

func TestGenerate_TCPPrimaryBackup(t *testing.T) {
	fresh := timePtr(time.Now())
	stale := timePtr(time.Now().Add(-time.Hour))

	freshCfg := Generate([]db.ResourceWithTargets{
		{
			Name: "raw", Protocol: "tcp", EntryPoint: "raw-tcp",
			Targets: []db.Target{
				{MeshIP: "100.100.0.1", Port: 9000, Role: "primary", LastSeen: fresh},
				{MeshIP: "100.100.0.2", Port: 9000, Role: "backup", LastSeen: fresh},
			},
		},
	})
	if got := freshCfg.TCP.Services["raw"].LoadBalancer.Servers[0].Address; got != "100.100.0.1:9000" {
		t.Errorf("fresh primary: expected primary address, got %q", got)
	}

	staleCfg := Generate([]db.ResourceWithTargets{
		{
			Name: "raw", Protocol: "tcp", EntryPoint: "raw-tcp",
			Targets: []db.Target{
				{MeshIP: "100.100.0.1", Port: 9000, Role: "primary", LastSeen: stale},
				{MeshIP: "100.100.0.2", Port: 9000, Role: "backup", LastSeen: fresh},
			},
		},
	})
	if got := staleCfg.TCP.Services["raw"].LoadBalancer.Servers[0].Address; got != "100.100.0.2:9000" {
		t.Errorf("stale primary: expected backup address, got %q", got)
	}

	if len(freshCfg.TCP.Services["raw"].LoadBalancer.Servers) != 1 {
		t.Errorf("expected exactly 1 server (master/backup, not both), got %d", len(freshCfg.TCP.Services["raw"].LoadBalancer.Servers))
	}
}

func TestGenerate_Empty(t *testing.T) {
	cfg := Generate(nil)
	if cfg.HTTP != nil || cfg.TCP != nil {
		t.Errorf("expected both nil for no resources, got %+v", cfg)
	}
}
