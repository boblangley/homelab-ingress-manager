# AGENTS.md

Guidance for coding agents working in this repository.

## What this is

A Caddy build (`cmd/homelab-ingress-manager`) that bundles caddy-docker-proxy
as a **dependency** and adds an OPNsense reconciler. Do not vendor or fork
caddy-docker-proxy code. Change behaviour through our own packages, or
upstream the change.

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/homelab-ingress-manager/` | `main`: Caddy standard modules + our command package |
| `internal/command/` | Registers `caddy ingress`: wraps caddy-docker-proxy's `docker-proxy` command (same flags) and starts the reconciler. `config.go` parses env vars. |
| `internal/labels/` | Pure label parsing: site addresses → host names, `gateway.portforward.*` → port forwards |
| `internal/reconciler/` | Stateless desired-vs-actual diff against OPNsense, driven by Docker events and a resync ticker |
| `internal/opnsense/` | Minimal OPNsense REST client (Unbound host overrides, destination NAT) |
| `internal/opnsense/opnsensetest/` | In-memory fake OPNsense API used by all tests |
| `e2e/` | Runs a pre-built image (`HLIM_IMAGE`) against the local Docker engine and the fake (`-tags e2e`) |
| `docs/` | User docs. Update them when env vars, labels or behaviour change. |

## Commands

```sh
gofmt -l .                 # must print nothing
go vet ./...
go test -race ./...
docker build -t homelab-ingress-manager:dev . && HLIM_IMAGE=homelab-ingress-manager:dev go test -tags e2e -count=1 ./e2e/
```

CI (`.github/workflows/ci.yaml`) runs all of the above. Releases
(`release.yaml`) run on tags matching `X.Y.Z`, with no `v` prefix.

## Rules of thumb

- **Never touch records we do not own.** Ownership is the description prefix
  `[homelab-ingress-manager]` / `[homelab-ingress-manager:<instance>]`. Any
  new record type must follow the same scheme.
- **Fail closed on Docker errors.** If any Docker host cannot be listed, skip
  the pass rather than deleting records.
- **OPNsense API facts** come from opnsense/core
  (`src/opnsense/mvc/app/models/OPNsense/{Unbound/Unbound.xml,Firewall/DNat.xml}`
  and the matching `Api/*Controller.php`). Check there before changing request
  shapes, and update `opnsensetest` to match. Known quirks: validation errors
  are HTTP 200 with `"result":"failed"`; NAT search rows use dotted keys; NAT
  search includes `is_automatic` rows; port ranges use `-`.
- Do not apply firewall changes with a revision argument. `apply/<revision>`
  arms OPNsense's rollback timer.
- No real OPNsense is available in CI. Behavioural changes need a test against
  `opnsensetest`.
- caddy-docker-proxy mutates `DOCKER_*` env vars while it starts. Create
  Docker clients before calling its command function, with explicit options.
- Keep dependencies minimal. Tests use the standard library only.
