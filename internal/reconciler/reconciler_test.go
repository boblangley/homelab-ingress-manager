package reconciler

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"go.uber.org/zap"

	"github.com/boblangley/homelab-ingress-manager/internal/opnsense"
	"github.com/boblangley/homelab-ingress-manager/internal/opnsense/opnsensetest"
)

type fakeDocker struct {
	mu         sync.Mutex
	containers []container.Summary
	listErr    error
	events     chan events.Message
	errs       chan error
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{events: make(chan events.Message, 10), errs: make(chan error, 1)}
}

func (f *fakeDocker) ContainerList(context.Context, container.ListOptions) ([]container.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]container.Summary(nil), f.containers...), f.listErr
}

func (f *fakeDocker) Events(context.Context, events.ListOptions) (<-chan events.Message, <-chan error) {
	return f.events, f.errs
}

func (f *fakeDocker) set(cs ...container.Summary) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers = cs
}

func ctr(name string, labels map[string]string) container.Summary {
	return container.Summary{ID: name + "0123456789abcdef", Names: []string{"/" + name}, Labels: labels}
}

type env struct {
	srv    *opnsensetest.Server
	local  *fakeDocker
	remote *fakeDocker
	r      *Reconciler
}

func setup(t *testing.T, cfg Config) *env {
	t.Helper()
	srv := opnsensetest.New(t)
	gw := opnsense.New(opnsense.Config{URL: srv.URL, APIKey: opnsensetest.APIKey, APISecret: opnsensetest.APISecret})
	local, remote := newFakeDocker(), newFakeDocker()
	hosts := []DockerHost{
		{Name: "local", IP: "192.168.1.10", API: local},
		{Name: "remote", IP: "192.168.1.20", API: remote},
	}
	if cfg.DNSTarget == "" {
		cfg.DNSTarget = "192.168.1.10"
	}
	return &env{srv: srv, local: local, remote: remote, r: New(cfg, gw, hosts, zap.NewNop())}
}

func (e *env) reconcile(t *testing.T) {
	t.Helper()
	if err := e.r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func (e *env) fqdns() []string {
	var out []string
	for _, h := range e.srv.HostOverrides() {
		out = append(out, h.Hostname+"."+h.Domain+"="+h.Server)
	}
	sort.Strings(out)
	return out
}

func (e *env) forwards() []string {
	var out []string
	for _, r := range e.srv.NATRules() {
		out = append(out, r.Protocol+":"+r.DestinationPort+"->"+r.Target+":"+r.LocalPort)
	}
	sort.Strings(out)
	return out
}

func equal(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReconcileCreatesAndRemovesRecords(t *testing.T) {
	e := setup(t, Config{})
	e.local.set(
		ctr("web", map[string]string{"caddy": "web.home.arpa", "caddy.reverse_proxy": "{{upstreams 80}}"}),
		ctr("plain", map[string]string{"other": "label"}),
	)
	e.remote.set(ctr("nas", map[string]string{
		"caddy_0":                        "nas.home.arpa files.home.arpa",
		"gateway.portforward.0.protocol": "tcp",
		"gateway.portforward.0.src_port": "2222",
		"gateway.portforward.0.dst_port": "22",
	}))

	e.reconcile(t)
	equal(t, e.fqdns(), []string{"files.home.arpa=192.168.1.10", "nas.home.arpa=192.168.1.10", "web.home.arpa=192.168.1.10"})
	equal(t, e.forwards(), []string{"tcp:2222->192.168.1.20:22"})
	rule := e.srv.NATRules()[0]
	if rule.Interface != "wan" || rule.DestinationNetwork != "wanip" || rule.SourceNetwork != "any" || rule.Pass != "pass" || rule.IPProtocol != "inet" {
		t.Errorf("unexpected rule fields %+v", rule)
	}
	if rule.Description != "[homelab-ingress-manager] nas" {
		t.Errorf("unexpected description %q", rule.Description)
	}
	if e.srv.Calls("/api/unbound/service/reconfigure") != 1 || e.srv.Calls("/api/firewall/d_nat/apply") != 1 {
		t.Error("expected one reconfigure and one apply")
	}

	// A second pass with no changes is a no-op.
	e.reconcile(t)
	if e.srv.Calls("/api/unbound/settings/add_host_override") != 3 || e.srv.Calls("/api/firewall/d_nat/add_rule") != 1 {
		t.Error("second reconcile should not add records")
	}
	if e.srv.Calls("/api/unbound/service/reconfigure") != 1 || e.srv.Calls("/api/firewall/d_nat/apply") != 1 {
		t.Error("second reconcile should not apply")
	}

	// Stopping the remote container removes its records.
	e.remote.set()
	e.reconcile(t)
	equal(t, e.fqdns(), []string{"web.home.arpa=192.168.1.10"})
	equal(t, e.forwards(), nil)
	if e.srv.Calls("/api/unbound/service/reconfigure") != 2 || e.srv.Calls("/api/firewall/d_nat/apply") != 2 {
		t.Error("removal should reconfigure and apply")
	}
}

func TestReconcileLeavesUnmanagedRecordsAlone(t *testing.T) {
	e := setup(t, Config{})
	e.srv.SeedHostOverride(opnsensetest.HostOverride{Enabled: "1", Hostname: "router", Domain: "home.arpa", RR: "A", Server: "192.168.1.1", Description: "manual"})
	e.srv.SeedHostOverride(opnsensetest.HostOverride{Enabled: "1", Hostname: "web", Domain: "home.arpa", RR: "A", Server: "192.168.1.99", Description: "manual"})
	e.srv.SeedNATRule(opnsensetest.NATRule{Interface: "wan", Protocol: "tcp", DestinationPort: "443", Target: "192.168.1.5", LocalPort: "443", Description: "manual https"})
	e.srv.SeedHostOverride(opnsensetest.HostOverride{Enabled: "1", Hostname: "other", Domain: "home.arpa", RR: "A", Server: "192.168.1.30", Description: "[homelab-ingress-manager:other] x"})

	e.local.set(ctr("web", map[string]string{
		"caddy":                          "web.home.arpa",
		"gateway.portforward.0.protocol": "tcp",
		"gateway.portforward.0.src_port": "443",
		"gateway.portforward.0.dst_port": "8443",
	}))
	e.reconcile(t)

	// web.home.arpa is already defined manually and port 443 is already
	// forwarded, so nothing is added; nothing that is not ours is removed.
	equal(t, e.fqdns(), []string{"other.home.arpa=192.168.1.30", "router.home.arpa=192.168.1.1", "web.home.arpa=192.168.1.99"})
	equal(t, e.forwards(), []string{"tcp:443->192.168.1.5:443"})
}

func TestReconcileRemovesDuplicatesAndStaleOwnedRecords(t *testing.T) {
	e := setup(t, Config{})
	tag := "[homelab-ingress-manager] old"
	e.srv.SeedHostOverride(opnsensetest.HostOverride{Enabled: "1", Hostname: "web", Domain: "home.arpa", RR: "A", Server: "192.168.1.10", Description: tag})
	e.srv.SeedHostOverride(opnsensetest.HostOverride{Enabled: "1", Hostname: "web", Domain: "home.arpa", RR: "A", Server: "192.168.1.10", Description: tag})
	// Points at an old caddy address: replaced.
	e.srv.SeedHostOverride(opnsensetest.HostOverride{Enabled: "1", Hostname: "api", Domain: "home.arpa", RR: "A", Server: "192.168.1.50", Description: tag})
	// Forward moved to another host: replaced.
	e.srv.SeedNATRule(opnsensetest.NATRule{Interface: "wan", Protocol: "udp", DestinationPort: "51820", Target: "192.168.1.20", LocalPort: "51820", Description: tag})

	e.local.set(ctr("web", map[string]string{
		"caddy":                          "web.home.arpa api.home.arpa",
		"gateway.portforward.0.protocol": "udp",
		"gateway.portforward.0.src_port": "51820",
		"gateway.portforward.0.dst_port": "51820",
	}))
	e.reconcile(t)
	equal(t, e.fqdns(), []string{"api.home.arpa=192.168.1.10", "web.home.arpa=192.168.1.10"})
	equal(t, e.forwards(), []string{"udp:51820->192.168.1.10:51820"})
}

func TestInstanceTagIsolation(t *testing.T) {
	e := setup(t, Config{Instance: "edge"})
	e.srv.SeedHostOverride(opnsensetest.HostOverride{Enabled: "1", Hostname: "a", Domain: "home.arpa", RR: "A", Server: "192.168.1.10", Description: "[homelab-ingress-manager] default instance"})
	e.reconcile(t)
	equal(t, e.fqdns(), []string{"a.home.arpa=192.168.1.10"})
	if e.r.Tag() != "[homelab-ingress-manager:edge]" {
		t.Errorf("unexpected tag %q", e.r.Tag())
	}
}

func TestDockerFailureSkipsReconcile(t *testing.T) {
	e := setup(t, Config{})
	e.local.set(ctr("web", map[string]string{"caddy": "web.home.arpa"}))
	e.reconcile(t)

	e.remote.listErr = errors.New("connection refused")
	e.local.set()
	if err := e.r.Reconcile(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	// The record must survive: a host being unreachable is not the same as
	// its containers being gone.
	equal(t, e.fqdns(), []string{"web.home.arpa=192.168.1.10"})
}

func TestDNSDisabledWithoutTarget(t *testing.T) {
	e := setup(t, Config{})
	e.r.cfg.DNSTarget = ""
	e.local.set(ctr("web", map[string]string{"caddy": "web.home.arpa"}))
	e.reconcile(t)
	equal(t, e.fqdns(), nil)
}

func TestIPv6DNSTarget(t *testing.T) {
	e := setup(t, Config{DNSTarget: "fd00::10"})
	e.local.set(ctr("web", map[string]string{"caddy": "web.home.arpa"}))
	e.reconcile(t)
	h := e.srv.HostOverrides()
	if len(h) != 1 || h[0].RR != "AAAA" || h[0].Server != "fd00::10" {
		t.Fatalf("unexpected overrides %+v", h)
	}
}

func TestPartialFailureStillApplies(t *testing.T) {
	e := setup(t, Config{})
	e.local.set(ctr("web", map[string]string{"caddy": "a.home.arpa b.home.arpa"}))
	e.srv.FailNext("/api/unbound/settings/add_host_override", 1)
	if err := e.r.Reconcile(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if len(e.srv.HostOverrides()) != 1 || e.srv.Calls("/api/unbound/service/reconfigure") != 1 {
		t.Fatal("successful additions should still be applied")
	}
	e.reconcile(t)
	equal(t, e.fqdns(), []string{"a.home.arpa=192.168.1.10", "b.home.arpa=192.168.1.10"})
}

func TestRunReactsToEvents(t *testing.T) {
	e := setup(t, Config{Debounce: 10 * time.Millisecond, ResyncInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.r.Run(ctx)

	waitFor(t, func() bool { return e.srv.Calls("/api/unbound/settings/search_host_override") >= 1 })
	e.remote.set(ctr("web", map[string]string{"caddy": "web.home.arpa"}))
	e.remote.events <- events.Message{Type: events.ContainerEventType, Action: events.ActionStart}
	waitFor(t, func() bool { return len(e.srv.HostOverrides()) == 1 })

	// A broken event stream triggers a resubscribe and a reconcile.
	e.remote.set()
	e.remote.errs <- errors.New("stream closed")
	waitFor(t, func() bool { return len(e.srv.HostOverrides()) == 0 })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
