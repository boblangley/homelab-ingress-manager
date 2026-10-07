// Package command registers the "ingress" Caddy subcommand, which runs
// caddy-docker-proxy's docker-proxy command alongside the OPNsense reconciler.
package command

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/caddyserver/caddy/v2"
	caddycmd "github.com/caddyserver/caddy/v2/cmd"
	dockerclient "github.com/docker/docker/client"
	"github.com/joho/godotenv"
	"go.uber.org/zap"

	// Registers the docker-proxy command and module. Imported here so its
	// init runs before ours.
	_ "github.com/lucaslorentz/caddy-docker-proxy/v2"

	"github.com/boblangley/homelab-ingress-manager/internal/opnsense"
	"github.com/boblangley/homelab-ingress-manager/internal/reconciler"
)

// Name is the subcommand that runs the ingress manager.
const Name = "ingress"

func init() {
	dockerProxy, ok := caddycmd.Commands()["docker-proxy"]
	if !ok {
		panic("caddy-docker-proxy did not register the docker-proxy command")
	}
	caddycmd.RegisterCommand(caddycmd.Command{
		Name:  Name,
		Usage: dockerProxy.Usage,
		Short: "Run caddy-docker-proxy and sync OPNsense DNS and port forwards from container labels",
		Long: `Runs caddy-docker-proxy exactly like "caddy docker-proxy" and accepts the same
flags and CADDY_* environment variables. When OPNSENSE_URL is set, it also keeps
Unbound host overrides and destination NAT rules in OPNsense in sync with the
labels of running containers.`,
		Flags: dockerProxy.Flags,
		Func: func(fl caddycmd.Flags) (int, error) {
			if err := startReconciler(fl); err != nil {
				return caddy.ExitCodeFailedStartup, err
			}
			return dockerProxy.Func(fl)
		},
	})
}

func logger() *zap.Logger { return caddy.Log().Named("ingress") }

func startReconciler(fl caddycmd.Flags) error {
	log := logger()

	// caddy-docker-proxy loads the env file later; load it now so gateway
	// settings can live there too. Existing variables are not overridden.
	envFile := os.Getenv("CADDY_DOCKER_ENVFILE")
	if envFile == "" {
		envFile = fl.String("envfile")
	}
	if envFile != "" {
		if err := godotenv.Load(envFile); err != nil {
			return fmt.Errorf("load env file %s: %w", envFile, err)
		}
	}

	settings, err := loadSettings(os.Getenv, flagValues{
		mode:            fl.String("mode"),
		labelPrefix:     fl.String("label-prefix"),
		dockerSockets:   fl.String("docker-sockets"),
		dockerCertsPath: fl.String("docker-certs-path"),
	})
	if err != nil {
		return err
	}
	if !settings.Enabled() {
		log.Info("OPNSENSE_URL not set; OPNsense integration disabled")
		return nil
	}
	if settings.Mode == "server" {
		log.Info("server mode; OPNsense integration runs on the controller")
		return nil
	}
	if settings.Reconciler.DNSTarget == "" {
		log.Warn("INGRESS_HOST_IP not set; DNS host overrides disabled")
	}

	// Build Docker clients now, before caddy-docker-proxy starts mutating
	// DOCKER_* environment variables for its own clients.
	hosts, err := dockerHosts(settings)
	if err != nil {
		return err
	}

	r := reconciler.New(settings.Reconciler, opnsense.New(settings.OPNsense), hosts, log)
	log.Info("OPNsense integration enabled",
		zap.String("url", settings.OPNsense.URL),
		zap.String("dnsTarget", settings.Reconciler.DNSTarget),
		zap.String("tag", r.Tag()),
		zap.Int("dockerHosts", len(hosts)))
	go r.Run(context.Background())
	return nil
}

func dockerHosts(s Settings) ([]reconciler.DockerHost, error) {
	sockets := s.DockerSocket
	if len(sockets) == 0 {
		sockets = []string{os.Getenv("DOCKER_HOST")}
	}
	hosts := make([]reconciler.DockerHost, 0, len(sockets))
	for i, socket := range sockets {
		opts := []dockerclient.Opt{dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation()}
		if socket != "" {
			opts = append(opts, dockerclient.WithHost(socket))
		}
		if i < len(s.DockerCerts) && s.DockerCerts[i] != "" {
			dir := s.DockerCerts[i]
			opts = append(opts, dockerclient.WithTLSClientConfig(
				filepath.Join(dir, "ca.pem"), filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")))
		}
		cli, err := dockerclient.NewClientWithOpts(opts...)
		if err != nil {
			return nil, fmt.Errorf("docker client for %q: %w", socket, err)
		}
		ip, err := hostIPForSocket(socket, s.HostIP, net.LookupIP)
		if err != nil {
			return nil, err
		}
		name := socket
		if name == "" {
			name = cli.DaemonHost()
		}
		hosts = append(hosts, reconciler.DockerHost{Name: name, IP: ip, API: cli})
	}
	return hosts, nil
}
