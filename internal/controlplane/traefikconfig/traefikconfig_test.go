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
			Targets: []db.Target{{Address: "100.100.0.1", Port: 8000, Role: "primary"}},
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
	if r.TLS != nil {
		t.Errorf("no cert_resolver set, expected no TLS block, got %+v", r.TLS)
	}
}

func TestGenerate_HTTPCertResolver(t *testing.T) {
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "app", Protocol: "http", Domain: strPtr("app.example.com"), EntryPoint: "web",
			CertResolver: strPtr("letsencrypt-staging"),
			Targets:      []db.Target{{Address: "100.100.0.1", Port: 8000, Role: "primary"}},
		},
	})

	r := cfg.HTTP.Routers["app"]
	if r.TLS == nil || r.TLS.CertResolver != "letsencrypt-staging" {
		t.Errorf("expected tls.certResolver \"letsencrypt-staging\", got %+v", r.TLS)
	}
}

func TestGenerate_HTTPMultiTargetSticky(t *testing.T) {
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "proxmox-ui", Protocol: "http", Domain: strPtr("pve.example.com"), EntryPoint: "web",
			Targets: []db.Target{
				{Address: "100.100.0.1", Port: 8006, Role: "primary"},
				{Address: "100.100.0.2", Port: 8006, Role: "primary"},
				{Address: "100.100.0.3", Port: 8006, Role: "primary"},
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

func TestGenerate_HTTPWildcardDomain(t *testing.T) {
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "dashboard-wildcard", Protocol: "http", Domain: strPtr("*.rv-tx.com"), EntryPoint: "websecure",
			Targets: []db.Target{{Address: "100.100.0.1", Port: 8080, Role: "primary"}},
		},
	})

	r := cfg.HTTP.Routers["dashboard-wildcard"]
	want := "HostRegexp(`^[^.]+\\.rv-tx\\.com$`)"
	if r.Rule != want {
		t.Errorf("got rule %q, want %q", r.Rule, want)
	}
}

func TestGenerate_HTTPWildcardDomainExplicitTLSDomain(t *testing.T) {
	// A HostRegexp() rule (what a wildcard domain compiles to) can't be
	// parsed by Traefik's automatic ACME domain detection -- without an
	// explicit tls.domains entry, a wildcard router's cert resolver
	// would never actually request a certificate. See hostRule and
	// routerTLS/tlsDomain's comments.
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "dashboard-wildcard", Protocol: "http", Domain: strPtr("*.rv-tx.com"), EntryPoint: "websecure",
			CertResolver: strPtr("letsencrypt"),
			Targets:      []db.Target{{Address: "100.100.0.1", Port: 8080, Role: "primary"}},
		},
	})

	r := cfg.HTTP.Routers["dashboard-wildcard"]
	if r.TLS == nil || len(r.TLS.Domains) != 1 || r.TLS.Domains[0].Main != "*.rv-tx.com" {
		t.Errorf("expected explicit tls.domains [{main: *.rv-tx.com}], got %+v", r.TLS)
	}
}

func TestGenerate_TCPCatchAll(t *testing.T) {
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "raw", Protocol: "tcp", Domain: nil, EntryPoint: "raw-tcp",
			Targets: []db.Target{{Address: "100.100.0.2", Port: 9000, Role: "primary"}},
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
				{Address: "100.100.0.1", Port: 9000, Role: "primary", LastSeen: fresh},
				{Address: "100.100.0.2", Port: 9000, Role: "backup", LastSeen: fresh},
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
				{Address: "100.100.0.1", Port: 9000, Role: "primary", LastSeen: stale},
				{Address: "100.100.0.2", Port: 9000, Role: "backup", LastSeen: fresh},
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

func TestGenerate_UDP(t *testing.T) {
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "dns-udp", Protocol: "udp", EntryPoint: "dns-udp",
			Targets: []db.Target{{Address: "192.168.7.242", Port: 53, Role: "primary", External: true}},
		},
	})

	if cfg.UDP == nil {
		t.Fatal("expected UDP config, got nil")
	}
	if cfg.HTTP != nil || cfg.TCP != nil {
		t.Fatal("expected no HTTP/TCP config for a udp-only resource")
	}
	r, ok := cfg.UDP.Routers["dns-udp"]
	if !ok {
		t.Fatal("expected router \"dns-udp\"")
	}
	if r.Service != "dns-udp" || r.EntryPoints[0] != "dns-udp" {
		t.Errorf("got router %+v", r)
	}
	svc := cfg.UDP.Services["dns-udp"]
	if svc.LoadBalancer.Servers[0].Address != "192.168.7.242:53" {
		t.Errorf("got server %+v", svc.LoadBalancer.Servers[0])
	}
}

func TestGenerate_ExternalTargetAlwaysPreferred(t *testing.T) {
	// An external primary has no heartbeat to go stale -- it should
	// always be preferred over the backup, unlike a node-backed primary.
	cfg := Generate([]db.ResourceWithTargets{
		{
			Name: "dns-tcp", Protocol: "tcp", EntryPoint: "dns-tcp",
			Targets: []db.Target{
				{Address: "192.168.7.242", Port: 53, Role: "primary", External: true, LastSeen: nil},
				{Address: "192.168.7.243", Port: 53, Role: "backup", External: true, LastSeen: nil},
			},
		},
	})
	if got := cfg.TCP.Services["dns-tcp"].LoadBalancer.Servers[0].Address; got != "192.168.7.242:53" {
		t.Errorf("expected external primary to always be preferred, got %q", got)
	}
}

func TestGenerate_Empty(t *testing.T) {
	cfg := Generate(nil)
	if cfg.HTTP != nil || cfg.TCP != nil || cfg.UDP != nil {
		t.Errorf("expected all nil for no resources, got %+v", cfg)
	}
}
