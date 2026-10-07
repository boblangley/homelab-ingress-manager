# How it works

```
                 Docker engine(s)
                   │ labels, events
        ┌──────────┴───────────┐
        ▼                      ▼
 caddy-docker-proxy      OPNsense reconciler ──► OPNsense API
 (Caddyfile → Caddy)     (DNS + port forwards)    Unbound overrides
                                                   Destination NAT
```

One binary, one process. `cmd/homelab-ingress-manager` is a Caddy build that
includes the standard modules, caddy-docker-proxy as a dependency, and the
`ingress` subcommand. `caddy ingress` builds the reconciler, then hands
control to caddy-docker-proxy's `docker-proxy` command with the same flags.
Neither side calls the other. Both read the same container labels.

## Reconciling

The reconciler keeps no state. On each pass it:

1. Lists running containers on every Docker host. If any host fails, the pass
   stops here, so an unreachable host never causes its records to be deleted.
2. Builds the desired host overrides (from site labels) and NAT rules (from
   `gateway.portforward.*`).
3. Reads all host overrides and NAT rules from OPNsense and keeps those whose
   description starts with this instance's tag.
4. Deletes owned records that are not desired (stale or duplicate). Adds
   desired records that are missing.
5. If anything changed, it reconfigures Unbound and/or reloads the firewall.

Records match on their key fields: name, record type and IP for DNS;
interface, protocol, WAN port, target and local port for NAT. Changing a label
replaces the record. A container rename alone does not.

Passes run:

- at startup,
- 2 seconds after a container `start` or `die` event (bursts are batched),
- after the Docker event stream reconnects,
- every `INGRESS_RESYNC_INTERVAL` (default 5 minutes), which also repairs
  drift from manual edits.

## Ownership and conflicts

Only records tagged by this instance are deleted or replaced. Before adding,
the reconciler checks what is already in OPNsense:

- **DNS:** if an untagged override, or another instance's override, already
  exists for the same name, it logs a warning and skips the name.
- **NAT:** if any other rule already forwards the same interface, protocol
  and WAN port, it logs a warning and skips the rule.

Set `INGRESS_INSTANCE` per deployment when several Docker hosts each run
their own copy against one OPNsense. Otherwise each copy would delete the
others' records.

## Multiple Docker hosts

With `CADDY_DOCKER_SOCKETS`, both caddy-docker-proxy and the reconciler watch
every listed engine. DNS for every container points at Caddy
(`INGRESS_DNS_TARGET`). Port forwards target the engine that runs the
container: `INGRESS_HOST_IP` for local sockets, the socket's host for remote
ones.

## Testing without OPNsense

`internal/opnsense/opnsensetest` is an in-memory fake of the endpoints we
use. It is modelled on opnsense/core's `Unbound.xml`, `DNat.xml` and their
API controllers. It reproduces OPNsense's quirks: validation errors come back
as HTTP 200 with `"result": "failed"`, NAT search rows use flattened keys
(`destination.port`), and automatic rules appear in search results. The
reconciler tests and the e2e test run against it.
