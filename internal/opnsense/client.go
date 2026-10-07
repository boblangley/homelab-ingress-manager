// Package opnsense is a minimal client for the parts of the OPNsense API that
// homelab-ingress-manager manages: Unbound host overrides and destination NAT
// (port forward) rules.
package opnsense

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config holds the connection settings for an OPNsense instance.
type Config struct {
	// URL is the base URL of the OPNsense web UI, e.g. https://192.168.1.1.
	URL       string
	APIKey    string
	APISecret string
	// InsecureSkipVerify disables TLS certificate verification. OPNsense ships
	// with a self-signed certificate, so this is common in homelabs.
	InsecureSkipVerify bool
	// Timeout for each API request. Defaults to 30s.
	Timeout time.Duration
}

// Client talks to the OPNsense REST API.
type Client struct {
	baseURL    string
	key        string
	secret     string
	httpClient *http.Client
}

// New creates a client from cfg.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for self-signed OPNsense certs
	}
	return &Client{
		baseURL:    strings.TrimRight(cfg.URL, "/"),
		key:        cfg.APIKey,
		secret:     cfg.APISecret,
		httpClient: &http.Client{Transport: transport, Timeout: timeout},
	}
}

// mutationResult is the body OPNsense returns from add/set/del endpoints.
// Validation failures are reported with HTTP 200 and result "failed".
type mutationResult struct {
	Result      string            `json:"result"`
	UUID        string            `json:"uuid,omitempty"`
	Validations map[string]string `json:"validations,omitempty"`
}

func (r mutationResult) err() error {
	switch r.Result {
	case "saved", "deleted":
		return nil
	}
	if len(r.Validations) > 0 {
		return fmt.Errorf("result %q, validations: %v", r.Result, r.Validations)
	}
	return fmt.Errorf("result %q", r.Result)
}

// searchRequest asks for every row in one page.
var searchRequest = map[string]any{"current": 1, "rowCount": -1}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(buf)
	} else {
		// OPNsense expects a JSON body on POST, even when there is nothing to send.
		reader = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.key, c.secret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("POST %s: decoding response: %w", path, err)
	}
	return nil
}

func (c *Client) mutate(ctx context.Context, path string, body any) (string, error) {
	var res mutationResult
	if err := c.post(ctx, path, body, &res); err != nil {
		return "", err
	}
	if err := res.err(); err != nil {
		return "", fmt.Errorf("POST %s: %w", path, err)
	}
	return res.UUID, nil
}
