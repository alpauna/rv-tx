# rv-tx

A custom HA Traefik-based reverse-proxy/tunnel control plane, replacing Pangolin. Single Go control plane coordinates a WireGuard mesh between nodes and generates Traefik's dynamic configuration for HTTP/TCP/UDP resources.

## Architecture

- `cmd/controlplane` — Postgres-backed WebSocket server: mesh coordination (node registration, IP allocation, peer list distribution) and Traefik dynamic config generation (`GET /traefik/config`), resource management (`POST /resources`).
- `cmd/agent` — runs on each mesh node: manages a local WireGuard interface (`wg`/`ip` directly, not a pure-Go stack), connects to the control plane over WebSocket.
- Traefik itself is used directly as the actual proxy — the control plane's only job is generating its dynamic config, not wrapping/replacing Traefik's own routing model.

Resources can target either a mesh node (`node_name`) or a raw external `ip:port` that never joins the mesh (`address`) — the latter is how the rv-tx.com DNS relay (below) reaches BIND9 without those boxes needing to join the mesh at all.

## Current deployment

- Control plane + one agent (`node-a-pg`): `pangolin-pg` VM, `192.168.1.108`, systemd services `rvtx-controlplane.service` / `rvtx-agent.service`, Postgres database `rvtx`.
- Second agent (`node-b-builder`): `builder`, `165.23.32.123`, systemd service `rvtx-agent.service`.
- DNS relay: Traefik container `rvtx-dns-relay` on `builder` (`--network host`, `--restart unless-stopped`), config at `/opt/rvtx/traefik/traefik.yml`, entrypoints `dns-tcp`/`dns-udp` on real port 53. Relays `rv-tx.com` DNS traffic to the internal BIND9 cluster (dns31/32/33, in the separate `dnsmasq-ui` project) via external-target TCP/UDP resources pointed at its dedicated public-view IPs (`192.168.7.242` primary / `192.168.7.243` backup).
- `rv-tx.com`'s real, delegated nameservers: `ns1.rv-tx.com` (`165.23.32.123`, `builder`'s relay above) and `ns2.rv-tx.com` (`165.23.32.238`, `dns03` directly — a real second public IP, not relayed through anything). Epik requires a minimum of two nameservers for a custom delegation, which is why there are two independent paths rather than one relay plus a backup.

## Milestones

1. Core mesh coordination (WireGuard, Postgres, WebSocket)
2. Traefik config generation for HTTP/TCP resources
3. Endpoint auto-discovery (control plane infers endpoint from connection source + agent-reported port, with a manual override escape hatch)
4. Multi-backend resources — HTTP sticky sessions + healthCheck failover, TCP/UDP master/backup
5. Self-hosted DNS-01 for `rv-tx.com` on the internal BIND9 cluster — done, `rv-tx.com` is genuinely delegated (confirmed at the authoritative `.com` registry, not just cached resolvers) to `ns1.rv-tx.com`/`ns2.rv-tx.com` (see dnsmasq-ui project's own memory/commits for the BIND9-side half of this work)
6. ACME DNS-01 automation via Traefik's native `rfc2136` provider (in progress)

## Known gaps

### IPv6 is only half-done for the DNS relay

`ns1.rv-tx.com` has a real, tracked AAAA record (`2001:48f8:20:0:d801:f956:c943:6d87`, builder's real public IPv6) — added via dnsmasq-ui's `dynamic_hosts` mechanism (a new poll-and-update entry, `connection: paramiko`, `interface: eth1`, `record_type: AAAA`, no `subnet`/SLAAC assumption, since this address is a dynamically-leased DHCPv6 `/128` that can change entirely on renewal, not a stable MAC-derived SLAAC suffix within a drifting prefix). It resolves correctly from anywhere.

**But the relay itself is not reachable over IPv6 from outside builder's own network** — confirmed both TCP and UDP time out externally on `[2001:48f8:20:0:d801:f956:c943:6d87]:53`, while the exact same queries against builder's own local IPv6 (from builder itself) succeed immediately, and this sandbox's own IPv6 egress was separately confirmed working (a real external IPv6 resolver answered fine). So Traefik is genuinely listening and answering on IPv6 — the gap is external reachability, most likely a Proxmox firewall rule that's IPv4-scoped only (the TCP/UDP:53 rule added for the IPv4 relay didn't carry an IPv6 counterpart; Proxmox typically keeps IPv4/IPv6 firewall rules separate).

**Deliberately left as a known gap for now** (user's explicit call, 2026-08-17), same as the Epik glue record below — IPv4 is fully proven end-to-end and that's what Phase 5's delegation actually depends on.

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

### `dns03` needed the same routing fix as `builder`, applied proactively this time

Once `dns03` got a real WAN interface for `ns2.rv-tx.com` (above), it became dual-homed the exact same way `builder` was — and would have hit the identical equal-metric default-route bug. Fixed proactively, before any live symptom, with the same pattern: `/etc/netplan/92-eth1-default-priority.yaml` giving `eth1` metric 50 and `eth0` metric 200. `dns03` didn't need `builder`'s extra specific-route fix (no `192.168.7.0/24`-style subnet it only reached by accident) — it's already directly on that subnet via `eth0.7`. Also needed a `systemctl restart named` to bind the new WAN IP's TCP:53 socket (UDP came up automatically) — same pattern as `builder`.

### dnsmasq drift can silently break BIND9's TCP:53 (AXFR) fleet-wide

Separate incident, same session, on the BIND9 side (dns31/32/33, `dnsmasq-ui` project): `dnsmasq` is deliberately left enabled at boot (not disabled) for historical reasons, but if it ever starts running again (e.g. after a reboot), it grabs the wildcard `0.0.0.0:53` bind and `named` ends up with **zero TCP listeners at all** — UDP mostly still works (kernel prefers the more-specific bind), but every AXFR (TCP-only) gets silently refused by dnsmasq itself, with zero trace in BIND's own logs. Check `ss -tlnp | grep dnsmasq` on any of the three DNS nodes if AXFR/TCP:53 issues show up again; `systemctl stop dnsmasq && systemctl restart named` is the fix (`named` needs an explicit restart to actually claim the freed TCP socket — stopping dnsmasq alone isn't enough).
