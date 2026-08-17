// Package traefikconfig builds Traefik's dynamic configuration JSON
// (https://doc.traefik.io/traefik/providers/http/) from the control
// plane's resources, for Traefik's HTTP provider to poll.
package traefikconfig

import (
	"fmt"

	"rv-tx/internal/controlplane/db"
)

type Config struct {
	HTTP *protocolConfig `json:"http,omitempty"`
	TCP  *protocolConfig `json:"tcp,omitempty"`
}

type protocolConfig struct {
	Routers  map[string]router  `json:"routers"`
	Services map[string]service `json:"services"`
}

type router struct {
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	EntryPoints []string `json:"entryPoints"`
}

type service struct {
	LoadBalancer loadBalancer `json:"loadBalancer"`
}

type loadBalancer struct {
	Servers []server `json:"servers"`
}

// server holds either URL (HTTP, e.g. "http://10.0.0.1:8000") or
// Address (TCP, e.g. "10.0.0.1:9000") -- Traefik expects different
// field names per protocol, only one is ever populated for a given
// entry depending which slice it's built into.
type server struct {
	URL     string `json:"url,omitempty"`
	Address string `json:"address,omitempty"`
}

// Generate builds the full dynamic config from every resource. Resource
// names must be unique (enforced at the DB level) since they double as
// both router and service names here.
func Generate(resources []db.ResourceWithNode) Config {
	var cfg Config

	for _, r := range resources {
		switch r.Protocol {
		case "http":
			if cfg.HTTP == nil {
				cfg.HTTP = &protocolConfig{Routers: map[string]router{}, Services: map[string]service{}}
			}
			rule := "PathPrefix(`/`)"
			if r.Domain != nil && *r.Domain != "" {
				rule = fmt.Sprintf("Host(`%s`)", *r.Domain)
			}
			cfg.HTTP.Routers[r.Name] = router{
				Rule:        rule,
				Service:     r.Name,
				EntryPoints: []string{r.EntryPoint},
			}
			cfg.HTTP.Services[r.Name] = service{
				LoadBalancer: loadBalancer{
					Servers: []server{{URL: fmt.Sprintf("http://%s:%d", r.TargetMeshIP, r.TargetPort)}},
				},
			}

		case "tcp":
			if cfg.TCP == nil {
				cfg.TCP = &protocolConfig{Routers: map[string]router{}, Services: map[string]service{}}
			}
			// TCP routers always need a rule; HostSNI(`*`) is the
			// documented catch-all for plain (non-TLS-terminated)
			// passthrough when no SNI-based domain match is wanted.
			rule := "HostSNI(`*`)"
			if r.Domain != nil && *r.Domain != "" {
				rule = fmt.Sprintf("HostSNI(`%s`)", *r.Domain)
			}
			cfg.TCP.Routers[r.Name] = router{
				Rule:        rule,
				Service:     r.Name,
				EntryPoints: []string{r.EntryPoint},
			}
			cfg.TCP.Services[r.Name] = service{
				LoadBalancer: loadBalancer{
					Servers: []server{{Address: fmt.Sprintf("%s:%d", r.TargetMeshIP, r.TargetPort)}},
				},
			}
		}
	}

	return cfg
}
