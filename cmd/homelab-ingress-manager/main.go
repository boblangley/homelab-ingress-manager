// Command homelab-ingress-manager is a Caddy build that bundles
// caddy-docker-proxy and an OPNsense reconciler. Run it with the "ingress"
// subcommand; every standard Caddy subcommand is available too.
package main

import (
	caddycmd "github.com/caddyserver/caddy/v2/cmd"

	// Standard Caddy modules.
	_ "github.com/caddyserver/caddy/v2/modules/standard"

	// caddy-docker-proxy plus the "ingress" subcommand.
	_ "github.com/boblangley/homelab-ingress-manager/internal/command"
)

func main() {
	caddycmd.Main()
}
