// Package vault reads secrets from HashiCorp Vault or OpenBao over HTTP(S) or
// a Unix socket, and resolves "vault:<path>#<field>" references.
package vault

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// RefPrefix marks a value as a Vault reference.
const RefPrefix = "vault:"

// Ref points at one field of a Vault secret.
type Ref struct {
	// Path is the API path under /v1/, e.g. secret/data/cloudflare for KV v2
	// or kv/cloudflare for KV v1.
	Path  string
	Field string
}

func (r Ref) String() string { return RefPrefix + r.Path + "#" + r.Field }

// ParseRef parses "vault:<path>#<field>". ok is false when s does not start
// with RefPrefix; err is set when it does but is malformed.
func ParseRef(s string) (ref Ref, ok bool, err error) {
	rest, ok := strings.CutPrefix(s, RefPrefix)
	if !ok {
		return Ref{}, false, nil
	}
	path, field, found := strings.Cut(rest, "#")
	path = strings.Trim(path, "/")
	if !found || path == "" || field == "" {
		return Ref{}, true, fmt.Errorf("invalid vault reference %q: want vault:<path>#<field>", s)
	}
	return Ref{Path: path, Field: field}, true, nil
}

// Config holds the connection settings. The field names follow the standard
// VAULT_* environment variables.
type Config struct {
	// Addr is http://, https:// or unix:///path/to/socket.
	Addr string
	// Token is sent as X-Vault-Token. Leave Token and TokenFile empty when a
	// proxy on the socket adds the token itself.
	Token string
	// TokenFile is read on every request, so a rotated token is picked up.
	// Ignored when Token is set.
	TokenFile          string
	Namespace          string
	CACert             string
	InsecureSkipVerify bool
	// Timeout for each request. Defaults to 30s.
	Timeout time.Duration
}

// ConfigFromEnv reads VAULT_ADDR, VAULT_TOKEN, VAULT_TOKEN_FILE,
// VAULT_NAMESPACE, VAULT_CACERT and VAULT_SKIP_VERIFY.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:      getenv("VAULT_ADDR"),
		Token:     getenv("VAULT_TOKEN"),
		TokenFile: getenv("VAULT_TOKEN_FILE"),
		Namespace: getenv("VAULT_NAMESPACE"),
		CACert:    getenv("VAULT_CACERT"),
	}
	if v := getenv("VAULT_SKIP_VERIFY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("VAULT_SKIP_VERIFY: %w", err)
		}
		cfg.InsecureSkipVerify = b
	}
	return cfg, nil
}

// Client reads secrets from the Vault HTTP API.
type Client struct {
	baseURL    string
	cfg        Config
	httpClient *http.Client
}

// New creates a client from cfg.
func New(cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("VAULT_ADDR is not set")
	}
	u, err := url.Parse(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("VAULT_ADDR: %w", err)
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	baseURL := strings.TrimRight(cfg.Addr, "/")
	switch u.Scheme {
	case "http", "https":
	case "unix":
		socket := u.Path
		if socket == "" {
			return nil, fmt.Errorf("VAULT_ADDR: %q has no socket path", cfg.Addr)
		}
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}
		baseURL = "http://localhost"
	default:
		return nil, fmt.Errorf("VAULT_ADDR: unsupported scheme in %q (want http, https or unix)", cfg.Addr)
	}
	if cfg.CACert != "" || cfg.InsecureSkipVerify {
		tlsCfg := &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // opt-in
		if cfg.CACert != "" {
			pem, err := os.ReadFile(cfg.CACert)
			if err != nil {
				return nil, fmt.Errorf("VAULT_CACERT: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("VAULT_CACERT: no certificates in %s", cfg.CACert)
			}
			tlsCfg.RootCAs = pool
		}
		transport.TLSClientConfig = tlsCfg
	}
	return &Client{
		baseURL:    baseURL,
		cfg:        cfg,
		httpClient: &http.Client{Transport: transport, Timeout: timeout},
	}, nil
}

func (c *Client) token() (string, error) {
	if c.cfg.Token != "" {
		return c.cfg.Token, nil
	}
	if c.cfg.TokenFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(c.cfg.TokenFile)
	if err != nil {
		return "", fmt.Errorf("VAULT_TOKEN_FILE: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Read returns the data of the secret at path. For KV v2 paths
// (<mount>/data/<name>) it returns the secret's fields, not the version
// metadata.
func (c *Client) Read(ctx context.Context, path string) (map[string]any, error) {
	token, err := c.token()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/"+strings.Trim(path, "/"), nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if c.cfg.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.cfg.Namespace)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Errors []string `json:"errors"`
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &e) == nil && len(e.Errors) > 0 {
			msg = strings.Join(e.Errors, "; ")
		}
		return nil, fmt.Errorf("read %s: status %d: %s", path, resp.StatusCode, msg)
	}
	var secret struct {
		Data map[string]any `json:"data"`
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&secret); err != nil {
		return nil, fmt.Errorf("read %s: decoding response: %w", path, err)
	}
	if secret.Data == nil {
		return nil, fmt.Errorf("read %s: no data", path)
	}
	// KV v2 wraps the fields as {"data": {...}, "metadata": {...}}.
	if inner, ok := secret.Data["data"].(map[string]any); ok {
		if _, ok := secret.Data["metadata"].(map[string]any); ok {
			return inner, nil
		}
	}
	return secret.Data, nil
}

// Resolve reads every ref and returns the field values keyed like refs.
// Each secret path is read once.
func (c *Client) Resolve(ctx context.Context, refs map[string]Ref) (map[string]string, error) {
	secrets := map[string]map[string]any{}
	out := make(map[string]string, len(refs))
	for key, ref := range refs {
		data, ok := secrets[ref.Path]
		if !ok {
			var err error
			if data, err = c.Read(ctx, ref.Path); err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			secrets[ref.Path] = data
		}
		v, ok := data[ref.Field]
		if !ok || v == nil {
			return nil, fmt.Errorf("%s: field %q not found in %s", key, ref.Field, ref.Path)
		}
		switch v := v.(type) {
		case string:
			out[key] = v
		case json.Number:
			out[key] = v.String()
		case bool:
			out[key] = strconv.FormatBool(v)
		default:
			return nil, fmt.Errorf("%s: field %q in %s is not a string", key, ref.Field, ref.Path)
		}
	}
	return out, nil
}
