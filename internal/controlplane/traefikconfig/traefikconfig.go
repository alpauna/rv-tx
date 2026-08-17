// Package traefikconfig builds Traefik's dynamic configuration JSON
// (https://doc.traefik.io/traefik/providers/http/) from the control
// plane's resources, for Traefik's HTTP provider to poll.
package traefikconfig

import (
	"fmt"
	"strings"
	"time"

	"rv-tx/internal/controlplane/db"
)

// primaryFreshness is how recent a tcp/udp primary target's node
// last_seen must be to still be considered up. 3x the agent's default
// 15s heartbeat interval -- generous enough to tolerate a couple of
// missed beats without flapping. This is node/mesh-level liveness
// (proves the primary's node is still in the mesh), not a check of the
// specific target port's application -- a coarse signal, deliberately,
// for this pass. Doesn't apply to external (non-mesh) targets at all --
// see selectMasterBackupTarget. Real per-port health (via Traefik's own
// healthCheck) and true client-IP-hash affinity for genuine multi-way
// TCP/UDP load balancing are both deferred, not solved here.
const primaryFreshness = 45 * time.Second

type Config struct {
	HTTP *protocolConfig    `json:"http,omitempty"`
	TCP  *protocolConfig    `json:"tcp,omitempty"`
	UDP  *udpProtocolConfig `json:"udp,omitempty"`
}

type protocolConfig struct {
	Routers           map[string]router           `json:"routers"`
	Services          map[string]service          `json:"services"`
	ServersTransports map[string]serversTransport `json:"serversTransports,omitempty"`
}

// serversTransport controls how Traefik connects to a service's own
// backend targets -- only ever populated here to skip TLS
// verification for an https target with a self-signed cert (e.g.
// Proxmox's own web UI, which doesn't get a real cert by default).
// Never used for the client-facing side of a router; that's
// routerTLS/certResolver, a completely separate concept.
type serversTransport struct {
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

type router struct {
	Rule        string     `json:"rule"`
	Service     string     `json:"service"`
	EntryPoints []string   `json:"entryPoints"`
	TLS         *routerTLS `json:"tls,omitempty"`
}

// routerTLS references a Traefik certificatesResolvers entry by name
// (static config, e.g. a staging vs. production Let's Encrypt
// resolver) -- only meaningful on HTTP routers.
type routerTLS struct {
	CertResolver string      `json:"certResolver,omitempty"`
	Domains      []tlsDomain `json:"domains,omitempty"`
}

// tlsDomain explicitly tells Traefik/lego which SAN to request a cert
// for. Needed for a wildcard resource ("*.example.com"): Traefik's
// automatic ACME domain detection only reads Host() rules, not
// HostRegexp() (which is what a wildcard domain compiles to, see
// hostRule) -- without this, a wildcard router's cert resolver would
// never actually request a certificate at all (confirmed live: the
// apex Host() router got its cert automatically, the HostRegexp()
// wildcard router sat there with tls.certResolver set and never even
// tried).
type tlsDomain struct {
	Main string `json:"main"`
}

type service struct {
	LoadBalancer loadBalancer `json:"loadBalancer"`
}

type loadBalancer struct {
	Servers          []server     `json:"servers"`
	Sticky           *sticky      `json:"sticky,omitempty"`
	HealthCheck      *healthCheck `json:"healthCheck,omitempty"`
	ServersTransport string       `json:"serversTransport,omitempty"`
}

type sticky struct {
	Cookie cookie `json:"cookie"`
}

type cookie struct {
	Name string `json:"name"`
}

type healthCheck struct {
	Path     string `json:"path,omitempty"`
	Interval string `json:"interval"`
	Timeout  string `json:"timeout"`
}

// server holds either URL (HTTP, e.g. "http://10.0.0.1:8000") or
// Address (TCP/UDP, e.g. "10.0.0.1:9000") -- Traefik expects different
// field names per protocol, only one is ever populated for a given
// entry depending which slice it's built into.
type server struct {
	URL     string `json:"url,omitempty"`
	Address string `json:"address,omitempty"`
}

// udpProtocolConfig mirrors protocolConfig but UDP routers have no
// "rule" field at all (confirmed via Traefik's docs -- UDP routers
// don't support rules, unlike HTTP's Host() or TCP's HostSNI()), so
// they need a distinct router shape rather than reusing router.
type udpProtocolConfig struct {
	Routers  map[string]udpRouter `json:"routers"`
	Services map[string]service   `json:"services"`
}

type udpRouter struct {
	Service     string   `json:"service"`
	EntryPoints []string `json:"entryPoints"`
}

// Generate builds the full dynamic config from every resource. Resource
// names must be unique (enforced at the DB level) since they double as
// both router and service names here.
func Generate(resources []db.ResourceWithTargets) Config {
	var cfg Config

	for _, r := range resources {
		switch r.Protocol {
		case "http":
			if cfg.HTTP == nil {
				cfg.HTTP = &protocolConfig{Routers: map[string]router{}, Services: map[string]service{}, ServersTransports: map[string]serversTransport{}}
			}
			rule := "PathPrefix(`/`)"
			if r.Domain != nil && *r.Domain != "" {
				rule = hostRule(*r.Domain)
			}
			var tls *routerTLS
			if r.CertResolver != nil && *r.CertResolver != "" {
				tls = &routerTLS{CertResolver: *r.CertResolver}
				if r.Domain != nil && strings.HasPrefix(*r.Domain, "*.") {
					tls.Domains = []tlsDomain{{Main: *r.Domain}}
				}
			}
			cfg.HTTP.Routers[r.Name] = router{
				Rule:        rule,
				Service:     r.Name,
				EntryPoints: []string{r.EntryPoint},
				TLS:         tls,
			}

			scheme := r.TargetScheme
			if scheme == "" {
				scheme = "http"
			}
			servers := make([]server, len(r.Targets))
			for i, t := range r.Targets {
				servers[i] = server{URL: fmt.Sprintf("%s://%s:%d", scheme, t.Address, t.Port)}
			}
			lb := loadBalancer{Servers: servers}
			if scheme == "https" && r.TargetSkipVerify {
				// insecureSkipVerify for a self-signed backend cert
				// (Proxmox's own web UI, TrueNAS, etc. don't get a
				// real cert by default) -- a per-resource
				// serversTransport, not global, so it doesn't weaken
				// TLS verification for any other resource's backend.
				transportName := r.Name + "-insecure"
				cfg.HTTP.ServersTransports[transportName] = serversTransport{InsecureSkipVerify: true}
				lb.ServersTransport = transportName
			}
			if r.Sticky {
				// Explicit per-resource opt-in (not inferred from
				// target count) -- pin each client to whichever server
				// it lands on first, and let Traefik re-pin to a
				// healthy one automatically on failure (confirmed
				// behavior, not assumed: Traefik's own docs state that
				// when the cookie-selected server becomes unhealthy,
				// the request is forwarded to a new server and the
				// cookie updated -- but that only happens once
				// healthCheck is configured too, so both go together).
				// Matters even for services like Proxmox's own web UI,
				// which ties its session/auth ticket to whichever node
				// actually issued it.
				lb.Sticky = &sticky{Cookie: cookie{Name: "rvtx_lb_" + r.Name}}
				lb.HealthCheck = &healthCheck{Path: "/", Interval: "10s", Timeout: "3s"}
			}
			cfg.HTTP.Services[r.Name] = service{LoadBalancer: lb}

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

			target := selectMasterBackupTarget(r.Targets)
			cfg.TCP.Services[r.Name] = service{
				LoadBalancer: loadBalancer{
					Servers: []server{{Address: fmt.Sprintf("%s:%d", target.Address, target.Port)}},
				},
			}

		case "udp":
			if cfg.UDP == nil {
				cfg.UDP = &udpProtocolConfig{Routers: map[string]udpRouter{}, Services: map[string]service{}}
			}
			cfg.UDP.Routers[r.Name] = udpRouter{
				Service:     r.Name,
				EntryPoints: []string{r.EntryPoint},
			}

			target := selectMasterBackupTarget(r.Targets)
			cfg.UDP.Services[r.Name] = service{
				LoadBalancer: loadBalancer{
					Servers: []server{{Address: fmt.Sprintf("%s:%d", target.Address, target.Port)}},
				},
			}
		}
	}

	return cfg
}

// hostRule turns a resource's domain into a Traefik router rule.
// Traefik's Host() matcher does exact-match only -- no wildcard syntax
// -- so a domain given as "*.example.com" is translated into a
// HostRegexp() matching any single label under that suffix instead.
// This is what lets one resource (e.g. the dashboard itself at
// rv-tx.com) also answer for every subdomain without a separate
// resource per hostname; a plain domain still gets the exact-match
// Host() rule as before.
func hostRule(domain string) string {
	if suffix, ok := strings.CutPrefix(domain, "*."); ok {
		escaped := strings.ReplaceAll(suffix, ".", `\.`)
		return fmt.Sprintf("HostRegexp(`^[^.]+\\.%s$`)", escaped)
	}
	return fmt.Sprintf("Host(`%s`)", domain)
}

// selectMasterBackupTarget implements the simple master/backup priority
// Traefik doesn't provide natively for TCP/UDP services: prefer the
// primary target as long as its node's mesh heartbeat is fresh,
// otherwise fall back to the backup. An external (non-mesh) primary has
// no heartbeat to check at all -- it's always preferred, since there's
// no liveness signal available here to fall back on (Traefik's own
// healthCheck, if configured on the entrypoint, is the real liveness
// mechanism for that case). With only one target, there's nothing to
// choose between.
func selectMasterBackupTarget(targets []db.Target) db.Target {
	if len(targets) == 1 {
		return targets[0]
	}

	var primary, backup db.Target
	for _, t := range targets {
		switch t.Role {
		case "primary":
			primary = t
		case "backup":
			backup = t
		}
	}

	if primary.External {
		return primary
	}
	if primary.LastSeen != nil && time.Since(*primary.LastSeen) <= primaryFreshness {
		return primary
	}
	return backup
}
