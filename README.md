# homelab-ingress-manager

Caddy for a homelab, configured from Docker labels, with OPNsense kept in
sync.

- **Reverse proxy:** runs [Caddy](https://caddyserver.com) with
  [caddy-docker-proxy](https://github.com/lucaslorentz/caddy-docker-proxy).
  Container labels become Caddy sites.
- **Local DNS:** every site address in those labels becomes an Unbound host
  override in OPNsense that points at Caddy.
- **Port forwards:** `gateway.portforward.*` labels become OPNsense
  destination NAT rules to the container's Docker host.

When a container stops, its DNS overrides and port forwards are removed.

## Quick start

1. Create an OPNsense API key ([OPNsense setup](docs/opnsense-setup.md)).
2. Copy [`docker-compose.yml`](docker-compose.yml) and
   [`.env.example`](.env.example) to the Docker host. Rename the env file to
   `.env` and fill it in.
3. `docker compose up -d`

The example `whoami` service then gets:

- a Caddy site at `https://whoami.home.arpa`,
- an Unbound override `whoami.home.arpa → INGRESS_HOST_IP`.

```yaml
services:
  whoami:
    image: traefik/whoami
    networks: [ingress]
    labels:
      caddy: whoami.home.arpa
      caddy.reverse_proxy: "{{upstreams 80}}"
      caddy.tls: internal
```

To expose a port to the internet:

```yaml
    ports: ["2222:22"]
    labels:
      gateway.portforward.0.protocol: tcp
      gateway.portforward.0.src_port: "2222"   # WAN port
      gateway.portforward.0.dst_port: "2222"   # Docker host port
```

## Documentation

- [Configuration](docs/configuration.md): environment variables and labels
- [OPNsense setup](docs/opnsense-setup.md): API user, version requirements
- [How it works](docs/how-it-works.md): reconcile loop, ownership, multiple hosts
- [Examples](examples/): port forwards, remote Docker hosts
- [caddy-docker-proxy README](https://github.com/lucaslorentz/caddy-docker-proxy#readme):
  every Caddy label and `CADDY_*` option

## Image

`ghcr.io/boblangley/homelab-ingress-manager`. Tags: `latest`, `X.Y.Z`,
`X.Y`, `X`. Platforms: `linux/amd64`, `linux/arm64`, `linux/arm/v7`,
`linux/arm/v6`.

The entrypoint is `caddy`, and the default command is `ingress`. Other Caddy
subcommands work too, e.g. `docker exec <container> caddy list-modules`.

## Development

```sh
go test -race ./...                      # unit tests, uses a fake OPNsense API
go run ./cmd/homelab-ingress-manager help ingress

docker build -t homelab-ingress-manager:dev .
HLIM_IMAGE=homelab-ingress-manager:dev go test -tags e2e ./e2e/   # needs Docker
```

### Releasing

Push a tag like `1.2.3`, with no `v` prefix. The release workflow pushes the
multi-arch image to GHCR and creates a GitHub release.

```sh
git tag 1.2.3 && git push origin 1.2.3
```

## License

Apache 2.0. caddy-docker-proxy is MIT licensed and used as a Go module
dependency.
