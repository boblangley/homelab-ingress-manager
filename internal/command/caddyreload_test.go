package command

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
)

func TestCaddyAdminAddr(t *testing.T) {
	addrs := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("10.200.0.5"), Mask: net.CIDRMask(16, 32)},
		}, nil
	}
	for _, tc := range []struct {
		name, mode, flag string
		env              map[string]string
		want             string
	}{
		{"default", "standalone", "", nil, "tcp/localhost:2019"},
		{"CADDY_ADMIN", "standalone", "", map[string]string{"CADDY_ADMIN": "0.0.0.0:2020"}, "tcp/0.0.0.0:2020"},
		{"CADDY_ADMIN unix", "server", "", map[string]string{"CADDY_ADMIN": "unix//run/caddy.sock"}, "unix//run/caddy.sock"},
		{"server controller network flag", "server", "10.200.0.0/16", nil, "tcp/10.200.0.5:2019"},
		{"server controller network env", "server", "", map[string]string{"CADDY_CONTROLLER_NETWORK": "10.200.0.0/16"}, "tcp/10.200.0.5:2019"},
		{"standalone ignores controller network", "standalone", "10.200.0.0/16", nil, "tcp/localhost:2019"},
	} {
		if got := caddyAdminAddr(tc.mode, env(tc.env), tc.flag, addrs); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestReloadCaddy runs a real Caddy instance and checks that reloadCaddy
// re-provisions the unchanged config.
func TestReloadCaddy(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	adminAddr := "tcp/" + ln.Addr().String()
	ln.Close()

	cfg := []byte(`{"admin":{"listen":"` + adminAddr + `"},"apps":{"http":{"servers":{"s":{"listen":["localhost:0"],"routes":[{"handle":[{"handler":"static_response","body":"ok"}]}]}}}}}`)
	if err := caddy.Load(cfg, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = caddy.Stop() })

	before := caddy.ActiveContext().Context
	if err := reloadCaddy(context.Background(), adminAddr); err != nil {
		t.Fatal(err)
	}
	if caddy.ActiveContext().Context == before {
		t.Fatal("config was not re-provisioned")
	}

	// The running config is unchanged.
	addr, _ := caddy.ParseNetworkAddress(adminAddr)
	resp, err := http.Get("http://" + addr.JoinHostPort(0) + "/config/apps/http/servers/s/routes/0/handle/0/body")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if got := bytes.TrimSpace(buf.Bytes()); string(got) != `"ok"` {
		t.Fatalf("config after reload: %s", got)
	}
}

func TestReloadCaddyUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := reloadCaddy(context.Background(), "tcp/"+addr); err == nil {
		t.Fatal("want error")
	}
}
