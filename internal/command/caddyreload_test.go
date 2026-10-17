package command

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"reflect"
	"testing"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
)

func TestCaddyAdminAddrs(t *testing.T) {
	addrs := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("10.200.0.5"), Mask: net.CIDRMask(16, 32)},
		}, nil
	}
	for _, tc := range []struct {
		name, flag string
		env        map[string]string
		want       []string
	}{
		{"default", "", nil, []string{"tcp/localhost:2019"}},
		{"CADDY_ADMIN", "", map[string]string{"CADDY_ADMIN": "0.0.0.0:2020"}, []string{"tcp/0.0.0.0:2020"}},
		{"CADDY_ADMIN unix", "10.200.0.0/16", map[string]string{"CADDY_ADMIN": "unix//run/caddy.sock"}, []string{"unix//run/caddy.sock"}},
		{"controller network flag", "10.200.0.0/16", nil, []string{"tcp/10.200.0.5:2019", "tcp/localhost:2019"}},
		{"controller network env", "", map[string]string{"CADDY_CONTROLLER_NETWORK": "10.200.0.0/16"}, []string{"tcp/10.200.0.5:2019", "tcp/localhost:2019"}},
		{"no local address in network", "192.168.0.0/24", nil, []string{"tcp/localhost:2019"}},
	} {
		if got := caddyAdminAddrs(env(tc.env), tc.flag, addrs); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestReloadCaddy runs a real Caddy instance and checks that reloadCaddy
// re-provisions the unchanged config.
func TestReloadCaddy(t *testing.T) {
	adminAddr := "tcp/" + freeAddr(t)

	cfg := []byte(`{"admin":{"listen":"` + adminAddr + `"},"apps":{"http":{"servers":{"s":{"listen":["localhost:0"],"routes":[{"handle":[{"handler":"static_response","body":"ok"}]}]}}}}}`)
	if err := caddy.Load(cfg, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = caddy.Stop() })

	// The first candidate is unreachable, as the controller-network address
	// is once a standalone controller has moved the admin API to localhost.
	before := caddy.ActiveContext().Context
	if err := reloadCaddy(context.Background(), []string{"tcp/" + freeAddr(t), adminAddr}); err != nil {
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
	if err := reloadCaddy(context.Background(), []string{"tcp/" + freeAddr(t), "tcp/" + freeAddr(t)}); err == nil {
		t.Fatal("want error")
	}
}

// freeAddr returns a localhost address with nothing listening on it.
func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}
