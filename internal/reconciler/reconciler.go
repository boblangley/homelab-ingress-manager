// Package reconciler keeps OPNsense host overrides and port forwards in sync
// with the labels of running Docker containers.
//
// Reconciliation is stateless: every pass computes the desired set of records
// from the running containers, lists the records this instance owns in
// OPNsense (identified by a tag at the start of their description), then adds
// what is missing and deletes what is no longer wanted. Records without the
// tag are never modified.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"go.uber.org/zap"

	"github.com/boblangley/homelab-ingress-manager/internal/labels"
	"github.com/boblangley/homelab-ingress-manager/internal/opnsense"
)

// DockerAPI is the subset of the Docker client the reconciler needs.
type DockerAPI interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	Events(ctx context.Context, options events.ListOptions) (<-chan events.Message, <-chan error)
}

// DockerHost is a Docker engine whose containers are reconciled.
type DockerHost struct {
	// Name identifies the host in logs, typically its socket URL.
	Name string
	// IP is the LAN address of the host, used as the port forward target.
	IP  string
	API DockerAPI
}

// Gateway is the subset of the OPNsense client the reconciler needs.
type Gateway interface {
	SearchHostOverrides(ctx context.Context) ([]opnsense.HostOverride, error)
	AddHostOverride(ctx context.Context, h opnsense.HostOverride) (string, error)
	DelHostOverride(ctx context.Context, uuid string) error
	ReconfigureUnbound(ctx context.Context) error
	SearchNATRules(ctx context.Context) ([]opnsense.NATRule, error)
	AddNATRule(ctx context.Context, r opnsense.NATRule) (string, error)
	DelNATRule(ctx context.Context, uuid string) error
	ApplyFirewall(ctx context.Context) error
}

// Config controls what the reconciler creates.
type Config struct {
	// LabelPrefix is the caddy-docker-proxy label prefix, usually "caddy".
	LabelPrefix string
	// DNSTarget is the IP that host overrides resolve to: the address Caddy
	// listens on. DNS reconciliation is disabled when empty.
	DNSTarget string
	// WANInterface is the OPNsense interface port forwards are created on.
	WANInterface string
	// Instance distinguishes records of several deployments sharing one
	// OPNsense. Each instance only manages records carrying its own tag.
	Instance string
	// ResyncInterval is how often a full reconcile runs without Docker events.
	ResyncInterval time.Duration
	// Debounce delays a reconcile after a Docker event to batch bursts.
	Debounce time.Duration
}

// Reconciler syncs container labels to OPNsense.
type Reconciler struct {
	cfg   Config
	gw    Gateway
	hosts []DockerHost
	log   *zap.Logger
	tag   string
}

// New creates a Reconciler.
func New(cfg Config, gw Gateway, hosts []DockerHost, log *zap.Logger) *Reconciler {
	if cfg.LabelPrefix == "" {
		cfg.LabelPrefix = "caddy"
	}
	if cfg.WANInterface == "" {
		cfg.WANInterface = "wan"
	}
	if cfg.ResyncInterval == 0 {
		cfg.ResyncInterval = 5 * time.Minute
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = 2 * time.Second
	}
	tag := "[homelab-ingress-manager]"
	if cfg.Instance != "" {
		tag = "[homelab-ingress-manager:" + cfg.Instance + "]"
	}
	return &Reconciler{cfg: cfg, gw: gw, hosts: hosts, log: log, tag: tag}
}

// Tag returns the description prefix marking records owned by this instance.
func (r *Reconciler) Tag() string { return r.tag }

func (r *Reconciler) owns(description string) bool {
	return strings.HasPrefix(description, r.tag)
}

func (r *Reconciler) describe(containerName, extra string) string {
	d := r.tag + " " + containerName
	if extra != "" {
		d += " - " + extra
	}
	return d
}

type runningContainer struct {
	name   string
	labels map[string]string
	hostIP string
}

// listContainers returns all running containers on all hosts. It fails if any
// host cannot be listed, so a temporarily unreachable host never causes its
// records to be deleted.
func (r *Reconciler) listContainers(ctx context.Context) ([]runningContainer, error) {
	var result []runningContainer
	for _, h := range r.hosts {
		list, err := h.API.ContainerList(ctx, container.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list containers on %s: %w", h.Name, err)
		}
		for _, c := range list {
			name := c.ID
			if len(name) > 12 {
				name = name[:12]
			}
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			result = append(result, runningContainer{name: name, labels: c.Labels, hostIP: h.IP})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result, nil
}

// Reconcile runs a single reconciliation pass.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	containers, err := r.listContainers(ctx)
	if err != nil {
		return err
	}
	return errors.Join(r.reconcileDNS(ctx, containers), r.reconcileNAT(ctx, containers))
}

func hostOverrideKey(h opnsense.HostOverride) string {
	return strings.ToLower(h.FQDN()) + "|" + h.RR + "|" + h.Server
}

func (r *Reconciler) desiredHostOverrides(containers []runningContainer) map[string]opnsense.HostOverride {
	desired := map[string]opnsense.HostOverride{}
	if r.cfg.DNSTarget == "" {
		return desired
	}
	rr := "A"
	if ip := net.ParseIP(r.cfg.DNSTarget); ip != nil && ip.To4() == nil {
		rr = "AAAA"
	}
	for _, c := range containers {
		for _, fqdn := range labels.Hostnames(c.labels, r.cfg.LabelPrefix) {
			hostname, domain, ok := labels.SplitFQDN(fqdn)
			if !ok {
				continue
			}
			h := opnsense.HostOverride{
				Enabled:     "1",
				Hostname:    hostname,
				Domain:      domain,
				RR:          rr,
				Server:      r.cfg.DNSTarget,
				Description: r.describe(c.name, ""),
			}
			if _, dup := desired[hostOverrideKey(h)]; !dup {
				desired[hostOverrideKey(h)] = h
			}
		}
	}
	return desired
}

func (r *Reconciler) reconcileDNS(ctx context.Context, containers []runningContainer) error {
	desired := r.desiredHostOverrides(containers)
	existing, err := r.gw.SearchHostOverrides(ctx)
	if err != nil {
		return err
	}

	var errs []error
	changed := false
	unmanaged := map[string]bool{}
	for _, h := range existing {
		if !r.owns(h.Description) {
			unmanaged[strings.ToLower(h.FQDN())] = true
			continue
		}
		key := hostOverrideKey(h)
		if _, want := desired[key]; want {
			delete(desired, key) // present; any duplicate row is deleted on a later iteration
			continue
		}
		if err := r.gw.DelHostOverride(ctx, h.UUID); err != nil {
			errs = append(errs, err)
			continue
		}
		changed = true
		r.log.Info("removed host override", zap.String("fqdn", h.FQDN()), zap.String("uuid", h.UUID))
	}

	for _, key := range sortedKeys(desired) {
		h := desired[key]
		if unmanaged[strings.ToLower(h.FQDN())] {
			r.log.Warn("host override exists but is not managed by this instance; leaving it alone",
				zap.String("fqdn", h.FQDN()))
			continue
		}
		uuid, err := r.gw.AddHostOverride(ctx, h)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		changed = true
		r.log.Info("added host override", zap.String("fqdn", h.FQDN()), zap.String("server", h.Server), zap.String("uuid", uuid))
	}

	if changed {
		if err := r.gw.ReconfigureUnbound(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func natKey(n opnsense.NATRule) string {
	return strings.Join([]string{n.Interface, n.Protocol, n.Destination.Port, n.Target, n.LocalPort}, "|")
}

// natListenKey identifies the WAN side of a rule, which must be unique.
func natListenKey(n opnsense.NATRule) string {
	return strings.Join([]string{n.Interface, n.Protocol, n.Destination.Port}, "|")
}

func (r *Reconciler) desiredNATRules(containers []runningContainer) map[string]opnsense.NATRule {
	desired := map[string]opnsense.NATRule{}
	for _, c := range containers {
		forwards, errs := labels.PortForwards(c.labels)
		for _, err := range errs {
			r.log.Warn("ignoring invalid port forward label", zap.String("container", c.name), zap.Error(err))
		}
		if len(forwards) == 0 {
			continue
		}
		if c.hostIP == "" {
			r.log.Warn("no host IP known for container's Docker host; skipping port forwards",
				zap.String("container", c.name))
			continue
		}
		for _, pf := range forwards {
			n := opnsense.NATRule{
				Disabled:    "0",
				Interface:   r.cfg.WANInterface,
				IPProtocol:  "inet",
				Protocol:    pf.Protocol,
				Source:      opnsense.Endpoint{Network: "any"},
				Destination: opnsense.Endpoint{Network: r.cfg.WANInterface + "ip", Port: pf.SrcPort},
				Target:      c.hostIP,
				LocalPort:   pf.DstPort,
				Pass:        "pass",
				Description: r.describe(c.name, pf.Description),
			}
			if _, dup := desired[natKey(n)]; !dup {
				desired[natKey(n)] = n
			}
		}
	}
	return desired
}

func (r *Reconciler) reconcileNAT(ctx context.Context, containers []runningContainer) error {
	desired := r.desiredNATRules(containers)
	existing, err := r.gw.SearchNATRules(ctx)
	if err != nil {
		return err
	}

	var errs []error
	changed := false
	listening := map[string]bool{}
	for _, n := range existing {
		if !r.owns(n.Description) {
			listening[natListenKey(n)] = true
			continue
		}
		key := natKey(n)
		if _, want := desired[key]; want {
			delete(desired, key)
			listening[natListenKey(n)] = true
			continue
		}
		if err := r.gw.DelNATRule(ctx, n.UUID); err != nil {
			errs = append(errs, err)
			continue
		}
		changed = true
		r.log.Info("removed port forward", zap.String("description", n.Description), zap.String("uuid", n.UUID))
	}

	for _, key := range sortedKeys(desired) {
		n := desired[key]
		if listening[natListenKey(n)] {
			r.log.Warn("another rule already forwards this WAN port; skipping",
				zap.String("protocol", n.Protocol), zap.String("port", n.Destination.Port),
				zap.String("description", n.Description))
			continue
		}
		uuid, err := r.gw.AddNATRule(ctx, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		listening[natListenKey(n)] = true
		changed = true
		r.log.Info("added port forward", zap.String("protocol", n.Protocol),
			zap.String("wanPort", n.Destination.Port), zap.String("target", n.Target),
			zap.String("localPort", n.LocalPort), zap.String("uuid", uuid))
	}

	if changed {
		if err := r.gw.ApplyFirewall(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Run reconciles once, then again after container start/stop events and on
// every resync interval, until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	trigger := make(chan struct{}, 1)
	poke := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	for _, h := range r.hosts {
		go r.watch(ctx, h, poke)
	}

	r.reconcileAndLog(ctx)

	ticker := time.NewTicker(r.cfg.ResyncInterval)
	defer ticker.Stop()
	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			if debounce == nil {
				debounce = time.After(r.cfg.Debounce)
			}
		case <-debounce:
			debounce = nil
			r.reconcileAndLog(ctx)
		case <-ticker.C:
			r.reconcileAndLog(ctx)
		}
	}
}

func (r *Reconciler) reconcileAndLog(ctx context.Context) {
	if err := r.Reconcile(ctx); err != nil && ctx.Err() == nil {
		r.log.Error("reconcile failed", zap.Error(err))
	}
}

// watch forwards container start/die events from one host to poke, and
// resubscribes with backoff when the event stream fails.
func (r *Reconciler) watch(ctx context.Context, h DockerHost, poke func()) {
	opts := events.ListOptions{Filters: filters.NewArgs(
		filters.Arg("type", string(events.ContainerEventType)),
		filters.Arg("event", string(events.ActionStart)),
		filters.Arg("event", string(events.ActionDie)),
	)}
	backoff := time.Second
	for ctx.Err() == nil {
		msgs, errs := h.API.Events(ctx, opts)
		healthy := true
		for healthy {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-msgs:
				if !ok {
					healthy = false
					continue
				}
				backoff = time.Second
				poke()
			case err := <-errs:
				if ctx.Err() != nil {
					return
				}
				r.log.Warn("docker event stream failed; resubscribing",
					zap.String("host", h.Name), zap.Duration("backoff", backoff), zap.Error(err))
				healthy = false
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
		poke() // events may have been missed while disconnected
	}
}
