package opnsense_test

import (
	"context"
	"strings"
	"testing"

	"github.com/boblangley/homelab-ingress-manager/internal/opnsense"
	"github.com/boblangley/homelab-ingress-manager/internal/opnsense/opnsensetest"
)

func newClient(t *testing.T) (*opnsensetest.Server, *opnsense.Client) {
	t.Helper()
	srv := opnsensetest.New(t)
	return srv, opnsense.New(opnsense.Config{URL: srv.URL + "/", APIKey: opnsensetest.APIKey, APISecret: opnsensetest.APISecret})
}

func TestHostOverrideLifecycle(t *testing.T) {
	srv, c := newClient(t)
	ctx := context.Background()

	uuid, err := c.AddHostOverride(ctx, opnsense.HostOverride{
		Enabled: "1", Hostname: "app", Domain: "home.arpa", RR: "A", Server: "192.168.1.10", Description: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.SearchHostOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UUID != uuid || rows[0].FQDN() != "app.home.arpa" || rows[0].Server != "192.168.1.10" {
		t.Fatalf("unexpected rows %+v", rows)
	}
	if err := c.DelHostOverride(ctx, uuid); err != nil {
		t.Fatal(err)
	}
	if err := c.ReconfigureUnbound(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.HostOverrides()); n != 0 {
		t.Fatalf("expected no overrides, got %d", n)
	}
	if srv.Calls("/api/unbound/service/reconfigure") != 1 {
		t.Fatal("reconfigure not called")
	}
}

func TestNATRuleLifecycle(t *testing.T) {
	srv, c := newClient(t)
	ctx := context.Background()

	want := opnsense.NATRule{
		Disabled: "0", Interface: "wan", IPProtocol: "inet", Protocol: "tcp",
		Source:      opnsense.Endpoint{Network: "any"},
		Destination: opnsense.Endpoint{Network: "wanip", Port: "2222"},
		Target:      "192.168.1.10", LocalPort: "22", Pass: "pass", Description: "ssh",
	}
	uuid, err := c.AddNATRule(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := c.SearchNATRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The fake also returns an automatic anti-lockout rule, which must be filtered out.
	want.UUID = uuid
	if len(rules) != 1 || rules[0] != want {
		t.Fatalf("got %+v, want [%+v]", rules, want)
	}
	if err := c.DelNATRule(ctx, uuid); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyFirewall(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.NATRules()); n != 0 {
		t.Fatalf("expected no rules, got %d", n)
	}
}

func TestValidationFailureIsAnError(t *testing.T) {
	_, c := newClient(t)
	_, err := c.AddHostOverride(context.Background(), opnsense.HostOverride{
		Enabled: "1", Hostname: "app", Domain: "home.arpa", RR: "A", Server: "not-an-ip",
	})
	if err == nil || !strings.Contains(err.Error(), "host.server") {
		t.Fatalf("expected validation error, got %v", err)
	}
	_, err = c.AddNATRule(context.Background(), opnsense.NATRule{Interface: "wan", Protocol: "tcp", Target: "x"})
	if err == nil || !strings.Contains(err.Error(), "rule.target") {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestDeleteMissingIsAnError(t *testing.T) {
	_, c := newClient(t)
	if err := c.DelHostOverride(context.Background(), "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestAuthFailure(t *testing.T) {
	srv := opnsensetest.New(t)
	c := opnsense.New(opnsense.Config{URL: srv.URL, APIKey: "wrong", APISecret: "wrong"})
	_, err := c.SearchHostOverrides(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}

func TestHTTPErrorIsAnError(t *testing.T) {
	srv, c := newClient(t)
	srv.FailNext("/api/firewall/d_nat/search_rule", 1)
	if _, err := c.SearchNATRules(context.Background()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error, got %v", err)
	}
}
