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
