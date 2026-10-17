package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
)

// caddyAdminAddrs returns the addresses the admin API of the Caddy server in
// this process may listen on, most specific first. caddy-docker-proxy starts
// the server (standalone or server mode) with CADDY_ADMIN, else its address in
// the controller network, else localhost:2019. A config the controller loads
// then moves the admin API to CADDY_ADMIN or the address the controller used
// to reach it, which is localhost:2019 in standalone mode. So with CADDY_ADMIN
// set it is the only address; otherwise both remaining ones are candidates.
func caddyAdminAddrs(getenv func(string) string, controllerNetwork string, interfaceAddrs func() ([]net.Addr, error)) []string {
	if v := strings.TrimSpace(getenv("CADDY_ADMIN")); v != "" {
		if !strings.Contains(v, "/") {
			v = "tcp/" + v
		}
		return []string{v}
	}
	var addrs []string
	if v := getenv("CADDY_CONTROLLER_NETWORK"); v != "" {
		controllerNetwork = v
	}
	if _, subnet, err := net.ParseCIDR(controllerNetwork); err == nil {
		ifAddrs, _ := interfaceAddrs()
		for _, a := range ifAddrs {
			if n, ok := a.(*net.IPNet); ok && subnet.Contains(n.IP) {
				addrs = append(addrs, "tcp/"+net.JoinHostPort(n.IP.String(), "2019"))
				break
			}
		}
	}
	return append(addrs, "tcp/localhost:2019")
}

// reloadCaddy reloads Caddy (see reloadCaddyAt) through the first admin
// address in adminAddrs that accepts a connection.
func reloadCaddy(ctx context.Context, adminAddrs []string) error {
	var err error
	for _, addr := range adminAddrs {
		err = reloadCaddyAt(ctx, addr)
		var opErr *net.OpError
		if err == nil || !errors.As(err, &opErr) || opErr.Op != "dial" {
			return err
		}
	}
	return err
}

// reloadCaddyAt makes the Caddy server at the admin address adminAddr
// re-provision its current config, so placeholders like {env.NAME} pick up
// changed environment variables. It uses If-Match, so a config loaded in the
// meantime (e.g. by caddy-docker-proxy) is never overwritten.
func reloadCaddyAt(ctx context.Context, adminAddr string) error {
	addr, err := caddy.ParseNetworkAddress(adminAddr)
	if err != nil {
		return fmt.Errorf("caddy admin address %q: %w", adminAddr, err)
	}
	transport := &http.Transport{}
	base := "http://" + addr.JoinHostPort(0)
	if addr.IsUnixNetwork() {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", addr.Host)
		}
		base = "http://localhost"
	}
	client := &http.Client{Transport: transport, Timeout: time.Minute}
	defer transport.CloseIdleConnections()

	const attempts = 3
	for i := 0; i < attempts; i++ {
		cfg, etag, err := adminGet(ctx, client, base+"/config/")
		if err != nil {
			return err
		}
		if trimmed := bytes.TrimSpace(cfg); len(trimmed) == 0 || string(trimmed) == "null" {
			return nil // nothing loaded yet; the first load will see the new values
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/config/", bytes.NewReader(cfg))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", etag)
		req.Header.Set("Cache-Control", "must-revalidate") // reload even though the JSON is unchanged
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("caddy reload: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			return nil
		case http.StatusPreconditionFailed:
			continue // config changed between GET and POST; try again
		default:
			return fmt.Errorf("caddy reload: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	return fmt.Errorf("caddy reload: config kept changing (%d attempts)", attempts)
}

func adminGet(ctx context.Context, client *http.Client, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("caddy admin: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("caddy admin: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("caddy admin: GET /config/: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, resp.Header.Get("Etag"), nil
}
