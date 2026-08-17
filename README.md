# rv-tx

A custom HA Traefik-based reverse-proxy/tunnel control plane, replacing Pangolin. Single Go control plane coordinates a WireGuard mesh between nodes and generates Traefik's dynamic configuration for HTTP/TCP/UDP resources.

## Architecture

- `cmd/controlplane` — Postgres-backed WebSocket server: mesh coordination (node registration, IP allocation, peer list distribution) and Traefik dynamic config generation (`GET /traefik/config`), resource management (`POST /resources`).
- `cmd/agent` — runs on each mesh node: manages a local WireGuard interface (`wg`/`ip` directly, not a pure-Go stack), connects to the control plane over WebSocket.
- Traefik itself is used directly as the actual proxy — the control plane's only job is generating its dynamic config, not wrapping/replacing Traefik's own routing model.

Resources can target either a mesh node (`node_name`) or a raw external `ip:port` that never joins the mesh (`address`) — the latter is how the rv-tx.com DNS relay (below) reaches BIND9 without those boxes needing to join the mesh at all.

### rv-route nodes: subnet relay

A node can advertise extra CIDRs beyond its own mesh IP (`RVTX_ADVERTISE_SUBNETS`, comma-separated) — every other agent then gets a real kernel route for that subnet via the mesh, automatically, with **no resource-model change needed at all**: a resource still just uses a plain external `address`, and it starts resolving through the relay the moment the relay node comes online, since this is genuinely just IP routing once the mesh has the route. Confirmed live, 2026-08-17: `node-a-pg` (which had *zero* other path to `192.168.7.0/24`) reached `192.168.7.11:8006` (Proxmox) with 0% packet loss and a real HTTP 200, purely through a relay node on that subnet — and confirmed the route is fully cleaned up (not leaked) when the relay disconnects.

**A relay node needs three things on its own host, none of them automated by the agent**:
1. `net.ipv4.ip_forward=1`.
2. `iptables -t nat -A POSTROUTING -s <mesh CIDR> -d <advertised subnet> -j MASQUERADE` — source NAT, deliberately, so the real LAN hosts being relayed to (Proxmox, TrueNAS, etc.) need **zero configuration of their own** — they see traffic as if it came from the relay node itself, which they already have a route to.
3. **An explicit `FORWARD` chain ACCEPT rule for the relay's interfaces.** This one is easy to miss and cost real debugging time live: if Docker is installed on the relay host (common — Docker sets the `FORWARD` chain's default policy to `DROP` and only opens holes for its own bridge), packets arrive on the WireGuard interface, get correctly decrypted, and then get silently eaten by the default-deny policy before NAT/routing ever gets a chance — `iptables -L FORWARD -n -v` will show the drop count climbing on the default policy line, with zero errors anywhere else to point at it. Fix: `iptables -I FORWARD -i <wg-iface> -o <lan-iface> -j ACCEPT` plus the reverse direction for established traffic.

- `dashboard/` — a React+Vite SPA, embedded into the control plane binary via `embed.FS` (`dashboard/embed.go`) and served at `/`. Everything under `/api/*` requires a session cookie proving a real per-user login (see "Accounts, roles, and email" below); `/ws/agent`, `/healthz`, and `/traefik/config` stay unauthenticated by design (agents and Traefik itself can't send credentials). `dashboard/dist/` is gitignored (build output) but must exist before `go build ./cmd/controlplane` will produce a real dashboard — run `npm --prefix dashboard ci && npm --prefix dashboard run build` first on a fresh clone.

### Editing an existing resource

`PUT /api/resources/{name}` replaces everything about a resource except its name (renaming isn't supported this way — delete and recreate instead) — same validation as create, so an edit can't leave a resource in a state that was never valid to begin with. The one real subtlety: `GET /api/resources`/`GET /api/nodes`-adjacent responses only ever showed a target's *resolved* address, never which mesh node it came from — editing a form built on that alone would've silently turned a live-tracked node into a frozen static address the moment you saved. Fixed by exposing `node_name` on each target in the API response (`null` for an external/non-mesh target), so the edit form can tell the two apart and preserve live tracking. Verified live: edited a resource with a mesh-node target (changed its entry point, turned sticky on) and confirmed the target was still resolving that node's live `mesh_ip` afterward, not a stale snapshot.

### Accounts, roles, and email

No open signup — every account is created by an admin inviting an email address (`POST /api/users`), which emails a one-time link (7-day expiry) to set a password. Two roles: `admin` (full read/write, including managing other users) and `viewer` (read-only — every mutating `/api/*` route 403s for a viewer session, enforced server-side in `requireAdmin`, not just hidden in the UI). Forgot-password works the same way (1-hour token) and deliberately returns an identical response whether or not the email is registered, so it can't be used to enumerate accounts.

Session cookies carry `{email, role, exp}` in the signed payload (`internal/controlplane/auth`) instead of a bare authenticated/not boolean — `GET /api/whoami` is how the SPA learns who's logged in on page load, since the cookie itself is HttpOnly and unreadable from JS.

The very first admin account is bootstrapped once, at startup, only when the `users` table is empty (`RVTX_BOOTSTRAP_ADMIN_EMAIL` + the pre-existing `RVTX_DASHBOARD_PASSWORD_HASH` from before per-user accounts existed) — every account after that goes through the normal invite flow. Email delivery config (`RVTX_SMTP_SERVER`/`PORT`/`USER`/`PASSWORD`/`FROM`) is reused verbatim from the `dnsmasq-ui` project's own `smtp.env` — same relay, same credentials, no new provisioning.

### HTTPS backend targets, with an optional skip-verify for self-signed certs

An `http`-protocol resource's `target_scheme` (`"http"`, the default, or `"https"`) controls how Traefik talks to the *backend*, completely independent of `cert_resolver` (which is about the *client-facing* side — see the ACME notes above). This is what lets a resource point at something that's HTTPS-only with no plain-HTTP listener at all, like Proxmox's own web UI (`:8006`) or TrueNAS. Since most internal appliances like that only ever get a self-signed cert, `target_skip_verify: true` adds a per-resource Traefik `serversTransport` with `insecureSkipVerify: true` — scoped to that one resource's backend connection only, never global, so it can't weaken TLS verification for any other resource's backend or for the client-facing side of any router.

### Sticky sessions are an explicit per-resource opt-in, not inferred from target count

`sticky` used to be implicit — any `http` resource with more than one target automatically got a load-balancer cookie. That's gone; it's now a real checkbox on the New Resource form (`sticky: true`/`false` in the API), independent of how many targets a resource has. Matters most for something like a multi-node Proxmox cluster used as one resource's target pool: Proxmox's own web UI ties its session/auth ticket to whichever node actually issued it, so without sticky sessions, round-robin load balancing would randomly break the UI mid-session. Sticky is allowed even on a single-target resource (harmless, simpler than special-casing it away) — verified live, both directions: a single-target resource with the box checked gets the cookie, a multi-target resource *without* it checked gets plain round-robin with no cookie at all.

### Multiple DNS names for one resource — what Traefik can and can't do

Traefik's own automatic ACME domain detection only reads a router's `Host()` rule — it can't request a cert for anything a `Host()` rule doesn't literally spell out, so out of the box one resource means one exact hostname. Two things this project adds on top of that, both in `internal/controlplane/traefikconfig`:

- **Wildcard domains** (`domain: "*.rv-tx.com"`): `hostRule()` compiles a leading `*.` into a `HostRegexp()` rule (`^[^.]+\.rv-tx\.com$`) instead of `Host()`, so one resource answers for any subdomain. Traefik still can't auto-derive an ACME SAN from a regex rule though (confirmed live, 2026-08-17: the equivalent `Host()` resource got its cert automatically, the `HostRegexp()` one just sat there with `tls.certResolver` set and never requested anything) — so a wildcard resource's router also gets an explicit `tls.domains: [{main: "*.rv-tx.com"}]`, which is what actually triggers the DNS-01 request for that SAN.
- **A DNS nameserver, specifically**: the dashboard's "Add DNS nameserver" wizard bundles the TCP+UDP port-53 external-target pattern (the same shape `ns1.rv-tx.com` uses) into one form. This is Traefik-side relay wiring only — the registrar side (adding a nameserver at Epik, glue records, the 2-nameserver minimum) is entirely outside Traefik's domain and still has to be done by hand.

What Traefik has no concept of at all: one resource serving multiple *unrelated* hostnames with different backends (e.g. `a.example.com` and `b.other-domain.com` on one router) — that's just two separate resources, which is already exactly how this project's data model works (one `domain` per resource).

## Current deployment

- Control plane + one agent (`node-a-pg`): `pangolin-pg` VM, `192.168.1.108`, systemd services `rvtx-controlplane.service` / `rvtx-agent.service`, Postgres database `rvtx`.
- Second agent (`node-b-builder`): `builder`, `165.23.32.123`, systemd service `rvtx-agent.service`.
- DNS relay: Traefik container `rvtx-dns-relay` on `builder` (`--network host`, `--restart unless-stopped`), config at `/opt/rvtx/traefik/traefik.yml`, entrypoints `dns-tcp`/`dns-udp` on real port 53. Relays `rv-tx.com` DNS traffic to the internal BIND9 cluster (dns31/32/33, in the separate `dnsmasq-ui` project) via external-target TCP/UDP resources pointed at its dedicated public-view IPs (`192.168.7.242` primary / `192.168.7.243` backup).
- `rv-tx.com`'s real, delegated nameservers: `ns1.rv-tx.com` (`builder`'s relay above, WAN IP has already changed once — see the operational note below, always verify current value rather than trusting this doc) and `ns2.rv-tx.com` (`165.23.32.238`, `dns03` directly — a real second public IP, not relayed through anything). Epik requires a minimum of two nameservers for a custom delegation, which is why there are two independent paths rather than one relay plus a backup.
- ACME: Traefik's static config on `builder` (`/opt/rvtx/traefik/traefik.yml`) has two `certificatesResolvers` (`letsencrypt-staging`, `letsencrypt`), both using the native `rfc2136` DNS-01 provider against the `rvtx-acme` TSIG key on dns01. An HTTP resource opts in via `cert_resolver: "letsencrypt-staging"` (or `"letsencrypt"`) in its `POST /resources` body.
- The dashboard itself is `rv-tx.com` and `*.rv-tx.com` — two HTTP resources (`rv-tx-com`, `rv-tx-com-wildcard`) targeting `node-a-pg:8080` (the control plane's own listen port) with `cert_resolver: "letsencrypt"` (production). Both real production certs issued live, 2026-08-17. The apex/wildcard A records (`165.23.33.26`, builder's WAN IP) are `dynamic_hosts`-tracked in dnsmasq-ui, same mechanism as `ns1.rv-tx.com`.

## Docker

`Dockerfile.controlplane` (multi-stage: Vite build → Go build, with the dashboard's `dist/` baked in and `migrations/` copied to `/opt/rvtx/migrations`) and `Dockerfile.agent` (Go build + `wireguard-tools`/`iproute2` installed in the runtime image, since the agent shells out to real `wg`/`ip` rather than a pure-Go WireGuard stack — see Architecture above). Config for both is env-vars-only either way; see `controlplane.env.example`/`agent.env.example`.

- `docker-compose.yml` — the control plane only. Postgres isn't included; point `RVTX_POSTGRES_DSN` at whatever Postgres already exists (this project's own deployment reuses one shared with other services, not a fresh instance).
- `docker-compose.agent.yml` — one agent, deployed separately on each mesh host (can't share a compose file with the control plane — a real mesh only exists across genuinely different machines). Requires `network_mode: host` + `cap_add: NET_ADMIN`, both non-optional: the agent creates/manages a real WireGuard netlink interface that has to be visible to the host's own routing table, which a bridge network can't provide. Verified live (2026-08-17, on `builder`): `wg genkey | wg pubkey` and `ip link add ... type wireguard` both work correctly inside the container with exactly this capability/network combination.

Both images build and the control plane has been smoke-tested end-to-end in a container (real migrations, real bootstrap-admin flow, `/healthz` 200) against a throwaway Postgres database — not yet used to replace either of the two systemd-deployed production instances (`pangolin-pg`, `builder`), which still run the bare binary. Migrating those over is a separate, deliberate step, not implied by this packaging existing.

## Milestones

1. Core mesh coordination (WireGuard, Postgres, WebSocket)
2. Traefik config generation for HTTP/TCP resources
3. Endpoint auto-discovery (control plane infers endpoint from connection source + agent-reported port, with a manual override escape hatch)
4. Multi-backend resources — HTTP sticky sessions + healthCheck failover, TCP/UDP master/backup
5. Self-hosted DNS-01 for `rv-tx.com` on the internal BIND9 cluster — done, `rv-tx.com` is genuinely delegated (confirmed at the authoritative `.com` registry, not just cached resolvers) to `ns1.rv-tx.com`/`ns2.rv-tx.com` (see dnsmasq-ui project's own memory/commits for the BIND9-side half of this work)
6. ACME DNS-01 automation via Traefik's native `rfc2136` provider — done, a real Let's Encrypt staging certificate has been obtained end-to-end through the real delegated zone (see the operational notes below for two real bugs found along the way)
7. Dashboard (React SPA, embedded + auth) + wildcard-domain support — done, `rv-tx.com`/`*.rv-tx.com` both serve the dashboard over real production HTTPS
8. Per-user accounts, roles, and email-based invite/password-reset — done, verified live end-to-end (invite → set password → login, forgot-password → reset → login with new password, and viewer-role 403s on every mutating route)
9. Docker packaging for both the control plane and the agent — done, both images build and the control plane is smoke-tested in a container; not yet used to replace the production systemd deployments
10. HTTPS backend targets + self-signed skip-verify — done, verified live against a real self-signed target (Proxmox's own web UI)
11. Sticky sessions as an explicit per-resource checkbox, decoupled from target count — done, verified live in both directions
12. Edit an existing resource (`PUT /api/resources/{name}`) — done, verified live that a mesh-node-backed target survives an edit as a live-tracked node, not frozen into a static address
13. rv-route subnet-relay nodes — done, verified live end-to-end against a real subnet a relay node sits on, including correct route cleanup on relay disconnect (see two real bugs found along the way in Operational notes)

## Known gaps

### Epik's glue records are IPv4-only

Epik's registrar-level glue/host records for both `ns1.rv-tx.com` and `ns2.rv-tx.com` only have A records (`165.23.32.123`/`165.23.32.238`), no AAAA — Epik's DNS-record API (the one `lego`'s built-in Epik provider would use) is a separate thing from registrar-level nameserver/glue management, which currently has to be done by hand through Epik's dashboard. Both nodes' real public IPv6 addresses are already tracked correctly in the zone's own content (see the `dynamic_hosts` entries below) — it's specifically the registrar-level glue that's IPv4-only. Automating this (or finding a better way to keep it in sync with dynamic IPv6) is future work, not scoped yet — "add a layer to Epik, or handle better later," per the user's own framing when this was raised.

### `ns2.rv-tx.com` exists because Epik requires two nameservers minimum

A single-nameserver custom delegation (`ns1.rv-tx.com` alone) never actually took effect at the registry — it looked like normal propagation delay for several hours (the glue record resolved fine, the dashboard showed it as saved) but a fresh `dig +trace` against the authoritative `.com` TLD servers kept showing the old `ns3/ns4.epik.com` the whole time, while a separately-submitted DNSSEC DS-record removal on the same domain propagated within minutes — which is what exposed that the *NS change itself* was the stuck part, not general registry-update slowness. Epik's minimum-nameserver-count requirement was the actual cause. `dns03` (previously LAN-only) got a real WAN interface brought up specifically to serve as this second, independent nameserver — not relayed through `builder` the way `ns1` is.

## Operational notes / known gotchas

### `builder` needed a routing fix for the DNS relay's UDP replies to work (2026-08-17)

`builder` is dual-homed (`eth0` = LAN `192.168.0.0/23`, `eth1` = WAN, public IP `165.23.32.123`). It shipped with **two default routes at the same DHCP-assigned metric**, one per interface. For any destination not covered by a more-specific route, the kernel's choice between two equal-metric defaults is effectively arbitrary — and this host was picking `eth0` even for genuinely external IPs.

This didn't affect TCP (the kernel pins a TCP reply to the connection's original local address/interface), but it silently broke UDP: a DNS reply to a real external querier would leave via `eth0` with the wrong source IP (`192.168.0.27` instead of `165.23.32.123`), and essentially every DNS client discards a UDP reply whose source IP doesn't match the address it queried — which looks exactly like a timeout, not an error.

**Fix**, in `/etc/netplan/92-eth1-default-priority.yaml`:
```yaml
network:
  version: 2
  ethernets:
    eth1:
      dhcp4-overrides:
        route-metric: 50
      dhcp6-overrides:
        route-metric: 50
    eth0:
      dhcp4-overrides:
        route-metric: 200
      dhcp6-overrides:
        route-metric: 200
      routes:
        - to: 192.168.7.0/24
          via: 192.168.0.1
```

Two parts, both required:
1. `eth1` gets a lower (preferred) default-route metric than `eth0`, so WAN-bound reply traffic reliably picks the right interface instead of an arbitrary tie.
2. A **specific** static route for `192.168.7.0/24` (the mgmt VLAN where BIND9's dedicated public-view IPs live) via `eth0`'s gateway. This subnet had **no route at all** before — it only ever worked by riding whichever default route happened to win the old ambiguous tie, so fixing #1 alone actually broke reachability to the BIND9 boxes until this was added.

**If you add a new subnet `builder` needs to reach** that isn't `192.168.0.0/23` or reachable via the WAN default, don't assume it works just because a similar one does — check `ip route show` for a specific route first, or add one to this same file.

Full incident writeup: `netmonitor_builder_asymmetric_routing` memory (netmonitor project). This is also the most likely real explanation for a previously-unresolved, separate bug (`netmonitor_wireguard_wan_bug`) on this same host — not yet retested against this fix.

### `builder` had the same *class* of bug for IPv6 too — but a different, worse mechanism (2026-08-17)

This was originally logged in "Known gaps" as "IPv6 is only half-done for the DNS relay," with a guessed cause ("most likely a Proxmox firewall rule that's IPv4-scoped only"). **That guess was wrong.** The real cause: `eth0` (meant to be a pure internal LAN interface — `dhcp6: true` in netplan's cloud-init defaults, never intentionally given a public role) was receiving real IPv6 Router Advertisements for the *same* `2001:48f8:20:0::/64` WAN prefix `eth1` uses, almost certainly RA leaking across the physical trunk from VLAN 999 onto VLAN 0/native — a switch/trunk-level detail invisible from either VM's own config. `eth0` picked up a real global address in that prefix via SLAAC, which made the kernel treat *any* destination sharing that `/64` — including every real external IPv6 client — as on-link and directly reachable via `eth0`. So every reply (a DNS answer, a TLS SYN-ACK) got routed out the LAN interface instead of back out `eth1` where the request actually arrived, and silently vanished. Confirmed live via `tcpdump` on both interfaces simultaneously during a real external client's connection attempt: the SYN arrived cleanly on `eth1`, but the SYN-ACK went out `eth0` instead (`ip -6 route get <client-address>` resolved via `eth0` with an `eth0`-scoped source address) — and `eth0` even had a *completed* Neighbor Discovery entry for the external client's address, meaning something on that LAN segment was answering ND for it, then the actual reply still went nowhere.

Same underlying bug *category* as the IPv4/UDP fix above (asymmetric routing silently breaking replies to external clients on a dual-homed host) but a different, more surprising mechanism — not an equal-metric tie this time, an actively wrong on-link determination caused by an interface acquiring an address it should never have had. Worth remembering as a recurring risk shape for any future dual-homed host in this project, IPv4 or IPv6: don't assume "it has an address so it must be intentional" — check *why* an interface has a given address before trusting routing decisions built on it.

**Fix**, `/etc/netplan/93-eth0-no-ipv6-autoconf.yaml`:
```yaml
network:
  version: 2
  ethernets:
    eth0:
      dhcp6: false
      accept-ra: false
```
`eth0` doesn't need any IPv6 address at all — this project's internal LAN routing is entirely IPv4. Removing its ability to acquire one removes the on-link ambiguity completely, without touching `eth1` or any switch/VLAN config. Verified live, both directions, from a real external client: HTTPS to `rv-tx.com`/`*.rv-tx.com` over IPv6 works end-to-end, and the DNS relay (`dig -6 @2001:48f8:20:0:7579:c7bf:94b7:7293 rv-tx.com SOA`) answers correctly too — this one fix covers every public-facing service on `builder`, not just the port being tested at the time, since the root cause was routing-table-wide.

### `dns03` needed the same routing fix as `builder`, applied proactively this time

Once `dns03` got a real WAN interface for `ns2.rv-tx.com` (above), it became dual-homed the exact same way `builder` was — and would have hit the identical equal-metric default-route bug. Fixed proactively, before any live symptom, with the same pattern: `/etc/netplan/92-eth1-default-priority.yaml` giving `eth1` metric 50 and `eth0` metric 200. `dns03` didn't need `builder`'s extra specific-route fix (no `192.168.7.0/24`-style subnet it only reached by accident) — it's already directly on that subnet via `eth0.7`. Also needed a `systemctl restart named` to bind the new WAN IP's TCP:53 socket (UDP came up automatically) — same pattern as `builder`.

### `builder`'s WAN IP changed mid-session (ISP-side) and exposed a real DNS-tracking gap

`builder`'s public IP changed from `165.23.32.123` to `165.23.33.26` (an ISP-side event, not something either of us did — same root event as the earlier packet-loss/outage). This broke `ns1.rv-tx.com`'s DNS records because of a real gap: the **A** record had only ever been set once (never tracked dynamically, unlike the AAAA), and the AAAA tracking entry's `target_host` (used for SSH reachability) was set to the WAN IP itself — so the exact moment the WAN IP changed, the poller could no longer even reach the host to ask about it. Both broke simultaneously.

**Fixed properly** (dnsmasq-ui side): added real `dynamic_hosts` A-record tracking for both `ns1`/`ns2` (previously only AAAA was tracked), and repointed both AAAA entries' `target_host` to each host's stable LAN IP (`192.168.0.27`/`192.168.0.233`) instead of its own WAN address. This decouples "how do I reach this host to ask a question" from "what WAN value am I asking about" — a future WAN IP change can't break its own detection mechanism again. Epik's glue A record for `ns1.rv-tx.com` still isn't automated (same known gap noted above) and needs manual updating whenever this happens again.

### DNS-01 update-policy: use `zonesub`, not `subdomain`, for multi-host certs

Phase 3's original update-policy (`grant rvtx-acme subdomain _acme-challenge.rv-tx.com. TXT;`) looked right but wasn't: `subdomain` only grants the exact name and *its own* subdomains, which is a completely different DNS-tree branch from `_acme-challenge.<hostname>.rv-tx.com` (a subdomain of `<hostname>.rv-tx.com`, not of `_acme-challenge.rv-tx.com`). Every real per-host challenge was REFUSED until this was changed to `grant rvtx-acme zonesub TXT;` (any name in the zone, TXT-only) on dns01 — correctly covers every hostname depth in one policy. If a future self-hosted zone needs "cert for any hostname under this domain," use `zonesub <type>`, not `subdomain <fixed-name>`.

### Don't trust public resolvers for DNS-01 propagation checks on a freshly-delegated zone

Traefik's `dnsChallenge.resolvers` initially pointed at `1.1.1.1`/`8.8.8.8` (common in examples) for propagation checking. Confirmed live via an isolated test (manually `nsupdate`-inserted a TXT record, then polled `1.1.1.1` in a tight loop for 60s) that Cloudflare's anycast network gave genuinely inconsistent `NXDOMAIN`/`NOERROR` answers depending on which physical node answered — likely stale negative caching on some nodes from before the zone existed. The record was correct and live on our own authoritative servers the entire time; only the public-resolver check was unreliable. Fixed by pointing `resolvers` at `rv-tx.com`'s own nameservers directly — always current, and it's what Let's Encrypt's real validators query anyway (the real delegation chain, not a public resolver's cache).

### lego's rfc2136 env var prefix is `DNSUPDATE_`, not `RFC2136_`

Confirmed directly against lego's source (bundled with Traefik 3.6.25) rather than assumed from older docs/examples: `DNSUPDATE_NAMESERVER`, `DNSUPDATE_TSIG_KEY`, `DNSUPDATE_TSIG_SECRET`, `DNSUPDATE_TSIG_ALGORITHM`. The provider name in `dnsChallenge.provider` is still `rfc2136` — only the env var prefix changed.

### systemd `Environment=` expands `$` in values — use `EnvironmentFile=` for secrets like bcrypt hashes

`Environment=RVTX_DASHBOARD_PASSWORD_HASH='$2a$10$...'` silently corrupted the value on service start — systemd's `Environment=` directive supports `$VARNAME`/`${VARNAME}` expansion referencing other environment variables, and a bcrypt hash's `$2a$10$...` structure looks exactly like that syntax to its parser, so `$2a`/`$10` got expanded to empty (undefined vars) instead of staying literal. The single quotes around the value did nothing to prevent this — that's shell quoting, and systemd's unit-file parser isn't a shell. Fixed by moving both the password hash and session secret to a separate `EnvironmentFile=/opt/rvtx/rvtx-controlplane.env` (`chmod 600`) instead — that directive loads `KEY=value` lines literally, with no `$`-expansion at all. Any future secret containing `$` (bcrypt/argon2 hashes, some base64 alphabets) needs `EnvironmentFile=`, not inline `Environment=`.

### SSH also mangles `$` in values passed as command-line arguments

A related trap while deploying the above: `ssh host bash -s -- "$HASH" "$SECRET" <<'EOF'` looked safe (the heredoc is quoted, so no *local* re-expansion) but still corrupted the hash, because OpenSSH's client joins all trailing arguments into a single string and sends that to the *remote* shell to parse before `bash -s` ever runs — so `$2a$10$...` gets `$`-expanded a second time, remotely, regardless of local quoting. There's no clean way to pass a `$`-containing secret as an SSH command-line argument. Fixed by writing the value to a local file and `scp`-ing it directly instead — no shell ever re-parses the content.

### `http.FileServer`'s SPA-fallback trick silently breaks any route deeper than `/`

The original SPA fallback rewrote an unmatched request's path to `/index.html` and re-invoked `http.FileServer` on it. That's a real, documented `http.FileServer` quirk waiting to bite: a literal `/index.html` request gets 301-redirected to `./` (its own special-case for "you don't need to spell out index.html"), and that redirect's `Location: ./` is relative to the *original* request path, not root. So `/accept-invite` (a real client-side route, an emailed invite link) resolved `./` against `/accept-invite`'s own directory and landed back at `/` — invisible for the root path itself (`/` redirecting to `/` looks like nothing happened), but silently broke every other client-side route, confirmed live: an invite link bounced straight to the login page instead of the accept-invite form. Fixed in `spaHandler()` by reading `index.html`'s bytes once at startup and serving them directly via `http.ServeContent` on a fallback, rather than ever asking `http.FileServer` to handle a path named exactly `index.html`. Worth remembering for any Go SPA fallback: never rewrite the request path to literally `index.html` and hand it back to `http.FileServer` — serve the content directly instead.

**A second, unrelated trap this exposed while testing the fix**: browsers cache 301 redirects aggressively and keyed to the exact URL (including query string) — after the bug above was fixed server-side, the *same browser tab* that had hit the broken link once kept re-applying the stale cached redirect locally, never even re-requesting the now-fixed URL from the server. A private/incognito window (no cache) confirmed the fix immediately; the regular tab needed a hard cache-clear for that specific URL. Any future SPA-fallback bug fix should be verified from a clean cache/incognito window, not the same tab that observed the original bug.

### `wg show <iface> allowed-ips` separates multiple CIDRs with a space, not a comma

Real bug, found live while verifying subnet-relay route cleanup (2026-08-17): `wgmanager`'s peer-removal path parses `wg show <iface> allowed-ips` to know which kernel routes to delete alongside a removed peer, and the original parser only ever looked at the second whitespace-separated field (`strings.Fields(line)[1]`), assuming that field held the *whole* comma-separated allowed-ips list the way `wg set`'s own *input* syntax works. It doesn't — the real output space-separates multiple CIDRs for one peer (confirmed directly: `PUBKEY\t100.100.0.3/32 192.168.7.0/24`, tab then two space-separated CIDRs), so a peer with more than one allowed-ips entry (any subnet-relay node) only ever had its *first* CIDR captured, silently dropping the rest from the removal loop. Result: removing a relay peer correctly removed the WireGuard peer entry itself, but leaked its subnet's kernel route forever (confirmed live — the route was still present, un-owned by any peer, after the peer itself was gone). Fixed by treating every field after the pubkey as its own CIDR (`fields[1:]`, not `fields[1]`) rather than assuming the comma-joined *input* format also describes this command's *output*. Worth remembering generally: a CLI tool's own flag/set syntax and its `show`/status output syntax are not guaranteed to match, even for the same underlying data — verify the actual output live rather than assuming symmetry.

### Docker silently defaults the `FORWARD` chain to DROP — breaks subnet-relay forwarding with no visible error

Also found live while setting up the first real subnet-relay test: a relay node that also runs Docker (common) has its `iptables` `FORWARD` chain default policy set to `DROP` by Docker's own setup, with explicit `ACCEPT` rules only for `docker0` traffic. Enabling `ip_forward` and adding a `POSTROUTING` `MASQUERADE` rule (the two commonly-documented steps for a NAT gateway) is **not sufficient** on such a host — packets arrive on the WireGuard interface, decrypt correctly, and then get silently dropped by the default `FORWARD` policy before NAT/routing ever runs. Nothing logs an error anywhere; the only visible symptom is the drop packet/byte counters climbing on the `DROP` policy line of `iptables -L FORWARD -n -v`, easy to miss unless checked specifically. Fixed with an explicit `iptables -I FORWARD -i <wg-iface> -o <lan-iface> -j ACCEPT` (plus the reverse direction for `RELATED,ESTABLISHED` traffic). Any future rv-route relay node setup on a Docker host needs this third step, not just the two usually-documented ones.

### dnsmasq drift can silently break BIND9's TCP:53 (AXFR) fleet-wide

Separate incident, same session, on the BIND9 side (dns31/32/33, `dnsmasq-ui` project): `dnsmasq` is deliberately left enabled at boot (not disabled) for historical reasons, but if it ever starts running again (e.g. after a reboot), it grabs the wildcard `0.0.0.0:53` bind and `named` ends up with **zero TCP listeners at all** — UDP mostly still works (kernel prefers the more-specific bind), but every AXFR (TCP-only) gets silently refused by dnsmasq itself, with zero trace in BIND's own logs. Check `ss -tlnp | grep dnsmasq` on any of the three DNS nodes if AXFR/TCP:53 issues show up again; `systemctl stop dnsmasq && systemctl restart named` is the fix (`named` needs an explicit restart to actually claim the freed TCP socket — stopping dnsmasq alone isn't enough).

### Editing a long-running app's config file out-of-band races its own background writes

Adding `rv-tx.com`/`*.rv-tx.com` A records to dnsmasq-ui's `zones.json` via a standalone script (bypassing its running Flask process) worked on disk, but the already-running `dnsmasq-ui.service` on dns01 still held the *old* config in memory — and its background `dynamic_hosts` poller called its own `save_config()`/deploy cycle shortly after, using that stale in-memory state, and silently clobbered the just-deployed zone file back to the old content. Confirmed by re-running the same deploy script a second time (no service restart needed) and diffing the zone file immediately after — the records were there that time. **General lesson**: editing a long-running service's config file directly from outside the process is only safe if the process is stopped first, or if you accept a race with whatever that process does on its own schedule (a poller, a periodic re-deploy, a webhook) — a bare file write can look like it worked and then get overwritten seconds later with no error anywhere.
