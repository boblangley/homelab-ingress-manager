package command

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/boblangley/homelab-ingress-manager/internal/opnsense"
	"github.com/boblangley/homelab-ingress-manager/internal/reconciler"
)

// Settings is the gateway configuration read from the environment, plus the
// caddy-docker-proxy options the reconciler shares with it.
type Settings struct {
	OPNsense   opnsense.Config
	Reconciler reconciler.Config
	// HostIP is the LAN address of the Docker host running this container.
	HostIP string
	// Mode is the caddy-docker-proxy mode: standalone, controller or server.
	Mode         string
	DockerSocket []string
	DockerCerts  []string
}

// Enabled reports whether OPNsense integration is configured.
func (s Settings) Enabled() bool { return s.OPNsense.URL != "" }

// flagValues are the docker-proxy command flags the reconciler reads.
type flagValues struct {
	mode, labelPrefix, dockerSockets, dockerCertsPath string
}

// loadSettings builds Settings from environment variables (via getenv) and
// docker-proxy flags, applying the same env-over-flag precedence as
// caddy-docker-proxy.
func loadSettings(getenv func(string) string, flags flagValues) (Settings, error) {
	envOr := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	splitList := func(v string) []string {
		if v == "" {
			return nil
		}
		return strings.Split(v, ",")
	}

	s := Settings{
		OPNsense: opnsense.Config{
			URL:       getenv("OPNSENSE_URL"),
			APIKey:    getenv("OPNSENSE_API_KEY"),
			APISecret: getenv("OPNSENSE_API_SECRET"),
		},
		Reconciler: reconciler.Config{
			LabelPrefix:  envOr("CADDY_DOCKER_LABEL_PREFIX", flags.labelPrefix),
			WANInterface: envOr("INGRESS_WAN_INTERFACE", "wan"),
			Instance:     getenv("INGRESS_INSTANCE"),
		},
		HostIP:       getenv("INGRESS_HOST_IP"),
		Mode:         envOr("CADDY_DOCKER_MODE", flags.mode),
		DockerSocket: splitList(envOr("CADDY_DOCKER_SOCKETS", flags.dockerSockets)),
		DockerCerts:  splitList(envOr("CADDY_DOCKER_CERTS_PATH", flags.dockerCertsPath)),
	}
	s.Reconciler.DNSTarget = envOr("INGRESS_DNS_TARGET", s.HostIP)

	if !s.Enabled() {
		return s, nil
	}
	if _, err := url.ParseRequestURI(s.OPNsense.URL); err != nil {
		return s, fmt.Errorf("OPNSENSE_URL: %w", err)
	}
	if s.OPNsense.APIKey == "" || s.OPNsense.APISecret == "" {
		return s, fmt.Errorf("OPNSENSE_API_KEY and OPNSENSE_API_SECRET are required when OPNSENSE_URL is set")
	}
	for key, ip := range map[string]string{"INGRESS_HOST_IP": s.HostIP, "INGRESS_DNS_TARGET": s.Reconciler.DNSTarget} {
		if ip != "" && net.ParseIP(ip) == nil {
			return s, fmt.Errorf("%s: %q is not an IP address", key, ip)
		}
	}
	if v := getenv("OPNSENSE_INSECURE_SKIP_VERIFY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return s, fmt.Errorf("OPNSENSE_INSECURE_SKIP_VERIFY: %w", err)
		}
		s.OPNsense.InsecureSkipVerify = b
	}
	if v := getenv("INGRESS_RESYNC_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return s, fmt.Errorf("INGRESS_RESYNC_INTERVAL: invalid duration %q", v)
		}
		s.Reconciler.ResyncInterval = d
	}
	return s, nil
}

// hostIPForSocket returns the address port forwards to containers behind
// socket should target: the host part of a remote socket URL, or HostIP for
// a local socket.
func hostIPForSocket(socket, hostIP string, lookup func(string) ([]net.IP, error)) (string, error) {
	u, err := url.Parse(socket)
	if socket == "" || err != nil || u.Scheme == "unix" || u.Scheme == "npipe" {
		return hostIP, nil
	}
	host := u.Hostname()
	if host == "" {
		return hostIP, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return hostIP, nil
		}
		return host, nil
	}
	ips, err := lookup(host)
	if err != nil {
		return "", fmt.Errorf("resolve docker host %q: %w", host, err)
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("docker host %q has no IPv4 address", host)
}
