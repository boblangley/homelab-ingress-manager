# Configuration

The container runs `caddy ingress`, which is
[caddy-docker-proxy](https://github.com/lucaslorentz/caddy-docker-proxy)'s
`docker-proxy` command plus the OPNsense reconciler. Every caddy-docker-proxy
flag and `CADDY_*` environment variable works unchanged; see its README for
those. This page covers what homelab-ingress-manager adds.

## Environment variables

| Variable | Default | Description |
| --- | --- | --- |
| `OPNSENSE_URL` | _(empty)_ | Base URL of OPNsense, e.g. `https://192.168.1.1`. When empty, OPNsense sync is off and the container is plain caddy-docker-proxy. |
| `OPNSENSE_API_KEY` | | API key. Required when `OPNSENSE_URL` is set. |
| `OPNSENSE_API_SECRET` | | API secret. Required when `OPNSENSE_URL` is set. |
| `OPNSENSE_INSECURE_SKIP_VERIFY` | `false` | Skip TLS verification. OPNsense uses a self-signed certificate by default. |
| `INGRESS_HOST_IP` | _(empty)_ | LAN IP of the Docker host running this container. DNS overrides resolve to it. Port forwards for containers on the local socket target it. DNS sync is off when it is empty. |
| `INGRESS_DNS_TARGET` | `INGRESS_HOST_IP` | Use a different IP for DNS overrides, e.g. a VIP in front of Caddy. An IPv6 address creates `AAAA` records. |
| `INGRESS_WAN_INTERFACE` | `wan` | OPNsense interface (internal name, e.g. `wan`, `opt1`) that port forwards listen on. The destination is that interface's address (`wanip`). |
| `INGRESS_INSTANCE` | _(empty)_ | Name for this deployment when several share one OPNsense. Each instance only touches records with its own tag (see [How it works](how-it-works.md)). |
| `INGRESS_RESYNC_INTERVAL` | `5m` | Full reconcile interval, on top of the event-driven ones. Go duration syntax. |

Shared with caddy-docker-proxy:

| Variable | Used for |
| --- | --- |
| `CADDY_DOCKER_LABEL_PREFIX` | Which labels hold site addresses (default `caddy`). |
| `CADDY_DOCKER_SOCKETS`, `CADDY_DOCKER_CERTS_PATH` | Docker hosts to watch. Each `tcp://` or `https://` socket's host part becomes the port forward target for its containers; hostnames are resolved to IPv4. |
| `CADDY_DOCKER_MODE` | In `server` mode the reconciler does not run; it runs on the `controller` (or `standalone`) instance. |
| `CADDY_DOCKER_ENVFILE` | Env file loaded at startup; the variables above can live there. |

## Secrets from Vault

Any environment variable can take its value from
[Vault](https://www.vaultproject.io) or [OpenBao](https://openbao.org)
instead of holding it directly. Set the variable to a reference:

```sh
OPNSENSE_API_SECRET=vault:secret/data/opnsense#api_secret
CLOUDFLARE_API_TOKEN=vault:secret/data/cloudflare#api_token
OIDC_CLIENT_SECRET=vault:secret/data/oidc#client_secret
```

The format is `vault:<path>#<field>`. `<path>` is the API path after `/v1/`:
`<mount>/data/<name>` for a KV v2 engine, `<mount>/<name>` for KV v1.
References are resolved when the container starts, before Caddy and the
reconciler. If any of them fails to resolve, the container exits.
`docker inspect` only shows the reference.

| Variable | Default | Description |
| --- | --- | --- |
| `VAULT_ADDR` | | `https://vault:8200`, `http://...`, or `unix:///path/to/vault.sock`. Required when a reference is used. |
| `VAULT_TOKEN` | | Token sent as `X-Vault-Token`. |
| `VAULT_TOKEN_FILE` | | File holding the token, e.g. a Docker secret or a Vault Agent sink. Read on every request, so rotation is picked up. Ignored when `VAULT_TOKEN` is set. |
| `VAULT_NAMESPACE` | | Sent as `X-Vault-Namespace`. |
| `VAULT_CACERT` | | PEM CA bundle for verifying an `https://` Vault. |
| `VAULT_SKIP_VERIFY` | `false` | Skip TLS verification. |
| `INGRESS_VAULT_REFRESH_INTERVAL` | `5m` | How often references are re-read. `0` reads them only at startup. |

The token is optional. Leave it unset when a proxy on the Unix socket adds
the token itself. [Wyrwood](https://github.com/wyrd-company/wyrwood) does
this: it sets `VAULT_ADDR=unix://.../vault.sock` in the container and
attaches the container's own token to each request.

### Rotation

On each refresh, changed values replace the old ones in the environment, then:

- `OPNSENSE_API_KEY` / `OPNSENSE_API_SECRET` are used by the next OPNsense
  request.
- Caddy re-provisions its current config through its admin API, so
  `{env.NAME}` placeholders see the new value. Use `{env.NAME}`, not
  `{$NAME}`, for secrets in labels and the base Caddyfile. `{$NAME}` is
  substituted once when the Caddyfile is adapted and is not refreshed.

If a refresh fails, the current values stay in place and a warning is logged.
If the Caddy reload fails, it is retried on every refresh until it succeeds.
Other settings, such as `OPNSENSE_URL`, are read only at startup. With
caddy-docker-proxy's controller/server split, `{env.NAME}` resolves on each
server, so give each server its own references.

## Container labels

### DNS host overrides

There are no extra labels for DNS. Every site address in a container's
caddy-docker-proxy site labels (`caddy`, `caddy_0`, `caddy_1`, ...) becomes an
Unbound host override pointing at `INGRESS_DNS_TARGET`:

```yaml
labels:
  caddy: app.home.arpa, www.app.home.arpa   # two overrides
  caddy.reverse_proxy: "{{upstreams 80}}"
```

Addresses are split into host and domain at the first dot (`app` +
`home.arpa`). These addresses are skipped: wildcards (`*.home.arpa`), IP
addresses, bare ports (`:80`), names without a dot (`localhost`),
placeholders (`{$HOST}`) and snippets (`(name)`). A scheme, port or path is
ignored (`https://app.home.arpa:8443` gives `app.home.arpa`).

| Label | Description |
| --- | --- |
| `gateway.dns=false` | Do not create DNS overrides for this container. |

### Port forwards

Each numbered group creates one destination NAT rule on the WAN interface:

| Label | Required | Description |
| --- | --- | --- |
| `gateway.portforward.<n>.protocol` | yes | `tcp`, `udp` or `tcp/udp` |
| `gateway.portforward.<n>.src_port` | yes | Port, or range like `27015-27020`, on the WAN side |
| `gateway.portforward.<n>.dst_port` | yes | Port on the Docker host. Usually a port the container publishes. |
| `gateway.portforward.<n>.description` | no | Appended to the rule description |

```yaml
ports:
  - "2222:22"
labels:
  gateway.portforward.0.protocol: tcp
  gateway.portforward.0.src_port: "2222"
  gateway.portforward.0.dst_port: "2222"
  gateway.portforward.0.description: Gitea SSH
```

Rules are created as: interface `INGRESS_WAN_INTERFACE`, IPv4, source `any`,
destination the interface address, redirect target the Docker host's IP, and
filter rule association **Pass**, so traffic is allowed without a separate
firewall rule.

Groups with missing or invalid values are logged and skipped. The other
groups on the same container are still applied.

## Bundled Caddy modules

The image includes Caddy's standard modules plus the ones below. Run
`docker exec <container> caddy list-modules` for the full list.

### Cloudflare DNS (`caddy-dns/cloudflare`)

[caddy-dns/cloudflare](https://github.com/caddy-dns/cloudflare) lets Caddy
solve ACME DNS challenges on zones hosted by Cloudflare. Use it for sites
that are not reachable on ports 80/443 from the internet, such as sites
published through a Cloudflare Tunnel.

Create a Cloudflare API token with `Zone.Zone:Read` and `Zone.DNS:Edit` on
the zone, pass it to the ingress container, and reference it from the site's
labels:

```yaml
services:
  ingress:
    environment:
      CLOUDFLARE_API_TOKEN: ${CLOUDFLARE_API_TOKEN}

  app:
    labels:
      caddy: app.example.com
      caddy.reverse_proxy: "{{upstreams 80}}"
      caddy.tls.dns: cloudflare {env.CLOUDFLARE_API_TOKEN}
```

### OIDC authentication (`greenpau/caddy-security`)

[caddy-security](https://github.com/greenpau/caddy-security) puts an OIDC or
OAuth login in front of reverse proxy targets. Its configuration is a global
`security` block, which is too nested to write comfortably as labels, so put
it in a base Caddyfile loaded with `CADDY_DOCKER_CADDYFILE_PATH`. Sites then
use two directives from labels:

| Label | Effect |
| --- | --- |
| `caddy.authenticate: with <portal>` | Serves the login portal on this site. |
| `caddy.authorize: with <policy>` | Requires a valid login for this site. |

[`examples/oidc/`](../examples/oidc/) is a complete setup: a portal at
`auth.home.arpa` backed by a generic OIDC provider, and a protected app. The
OIDC client secret is passed to the ingress container as an environment
variable and referenced as `{env.OIDC_CLIENT_SECRET}` in the base
Caddyfile. See the
[caddy-security docs](https://docs.authcrunch.com/) for identity providers,
policies and user transforms.

### TCP/UDP routing (`mholt/caddy-l4`)

[caddy-l4](https://github.com/mholt/caddy-l4) adds the `layer4` app for raw
TCP/UDP routing, proxying, protocol matching, and TLS passthrough or termination.
Configure its global `layer4` block in a base Caddyfile loaded with
`CADDY_DOCKER_CADDYFILE_PATH`, alongside your HTTP sites. For example:

```caddyfile
{
    layer4 {
        :5000 {
            route {
                proxy app:5000
            }
        }
    }
}
```

Publish each listener's port from the ingress container (for this example,
`5000:5000/tcp`) and make the upstream reachable from its Docker network.
Layer 4 listeners do not create OPNsense records automatically; DNS host
overrides still come from site labels and NAT rules from `gateway.portforward.*`
labels. See the [caddy-l4 documentation](https://github.com/mholt/caddy-l4/tree/master/docs)
for matchers, handlers, and UDP listener configuration.
