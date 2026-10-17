# Changelog

## 0.2.0

### Features

- Bundle caddy-security for OIDC and OAuth authentication and authorization in front of upstream services.
- Bundle caddy-l4 for raw TCP/UDP routing and proxying, protocol matching, and TLS passthrough or termination through the layer4 app.
- Bundle the Cloudflare DNS provider for ACME DNS challenges on Cloudflare-managed zones.
- Resolve vault: environment references from Vault or OpenBao, refresh secrets while running, and reload Caddy when secrets change.

### Fixes

- Validate Intentional release state in CI, publish release notes from the changelog, and enable emulation for all supported container architectures.
- Retry failed Caddy reloads after secret refresh, honor the configured admin address, and correct the OIDC example ports.

## 0.1.0

### Features

- First release of homelab-ingress-manager: a standalone Caddy build with caddy-docker-proxy and an OPNsense reconciler that manages Unbound DNS host overrides and destination NAT port forwards from Docker labels, with multi-host support and ownership-scoped reconciliation.
