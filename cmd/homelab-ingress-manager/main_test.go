package main

import (
	"testing"

	"github.com/caddyserver/caddy/v2"
)

// TestModulesRegistered guards the non-standard Caddy modules bundled in
// this build.
func TestModulesRegistered(t *testing.T) {
	for _, id := range []string{
		"dns.providers.cloudflare",
		"layer4",
		"layer4.handlers.echo",
		"layer4.handlers.proxy",
		"layer4.handlers.tls",
		"layer4.matchers.tls",
		"security",
		"http.handlers.authenticator",
		"http.handlers.authorization",
	} {
		if _, err := caddy.GetModule(id); err != nil {
			t.Errorf("module %s: %v", id, err)
		}
	}
}
