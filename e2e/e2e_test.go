//go:build e2e

// Package e2e runs the built image against the local Docker engine and a fake
// OPNsense API. It needs Docker and an image tag in HLIM_IMAGE:
//
//	docker build -t homelab-ingress-manager:dev .
//	HLIM_IMAGE=homelab-ingress-manager:dev go test -tags e2e ./e2e/
package e2e

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/boblangley/homelab-ingress-manager/internal/opnsense/opnsensetest"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestEndToEnd(t *testing.T) {
	image := os.Getenv("HLIM_IMAGE")
	if image == "" {
		t.Skip("HLIM_IMAGE not set")
	}
	fake := opnsensetest.New(t)

	// Host networking lets the container reach the fake API on 127.0.0.1.
	manager := docker(t, "run", "-d", "--network", "host",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-e", "OPNSENSE_URL="+fake.URL,
		"-e", "OPNSENSE_API_KEY="+opnsensetest.APIKey,
		"-e", "OPNSENSE_API_SECRET="+opnsensetest.APISecret,
		"-e", "INGRESS_HOST_IP=192.168.1.10",
		"-e", "INGRESS_INSTANCE=e2e",
		"-e", "CADDY_ADMIN=127.0.0.1:2019",
		image)
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", manager).CombinedOutput()
			t.Logf("manager logs:\n%s", logs)
		}
		exec.Command("docker", "rm", "-f", manager).Run()
	})

	app := docker(t, "run", "-d",
		"-l", "caddy=whoami.home.arpa",
		"-l", "caddy.respond=hello",
		"-l", "gateway.portforward.0.protocol=tcp",
		"-l", "gateway.portforward.0.src_port=2222",
		"-l", "gateway.portforward.0.dst_port=22",
		"-l", "gateway.portforward.0.description=e2e ssh",
		"alpine:3.22", "sleep", "300")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", app).Run() })

	waitFor(t, "host override", 30*time.Second, func() bool {
		h := fake.HostOverrides()
		return len(h) == 1 && h[0].Hostname == "whoami" && h[0].Domain == "home.arpa" && h[0].Server == "192.168.1.10"
	})
	waitFor(t, "port forward", 30*time.Second, func() bool {
		r := fake.NATRules()
		return len(r) == 1 && r[0].DestinationPort == "2222" && r[0].Target == "192.168.1.10" && r[0].LocalPort == "22" &&
			r[0].Description == "[homelab-ingress-manager:e2e] "+strings.TrimPrefix(docker(t, "inspect", "-f", "{{.Name}}", app), "/")+" - e2e ssh"
	})

	// caddy-docker-proxy picked up the same labels.
	waitFor(t, "caddy config", 30*time.Second, func() bool {
		resp, err := http.Get("http://127.0.0.1:2019/config/")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(body), "whoami.home.arpa")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", app).CombinedOutput(); err != nil {
		t.Fatalf("remove app: %v %s", err, out)
	}
	waitFor(t, "records removed", 30*time.Second, func() bool {
		return len(fake.HostOverrides()) == 0 && len(fake.NATRules()) == 0
	})
}
