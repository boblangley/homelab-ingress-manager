package vault

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeVault serves secrets at /v1/<path>. Paths under a "data" segment are
// answered in the KV v2 shape.
type fakeVault struct {
	mu        sync.Mutex
	secrets   map[string]map[string]any
	token     string // required X-Vault-Token; empty accepts any
	namespace string // last X-Vault-Namespace seen
	reads     int
}

func (f *fakeVault) set(path string, data map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[path] = data
}

func (f *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	f.namespace = r.Header.Get("X-Vault-Namespace")
	if f.token != "" && r.Header.Get("X-Vault-Token") != f.token {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	data, ok := f.secrets[path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[]}`))
		return
	}
	body := map[string]any{"data": data}
	if strings.Contains(path, "/data/") {
		body = map[string]any{"data": map[string]any{"data": data, "metadata": map[string]any{"version": 1}}}
	}
	_ = json.NewEncoder(w).Encode(body)
}

func newFakeVault() *fakeVault {
	return &fakeVault{secrets: map[string]map[string]any{}}
}

func TestParseRef(t *testing.T) {
	ref, ok, err := ParseRef("vault:secret/data/cloudflare#api_token")
	if !ok || err != nil || ref != (Ref{Path: "secret/data/cloudflare", Field: "api_token"}) {
		t.Fatalf("got %+v ok=%v err=%v", ref, ok, err)
	}
	if _, ok, _ := ParseRef("plain-value"); ok {
		t.Fatal("plain value parsed as a ref")
	}
	for _, bad := range []string{"vault:", "vault:secret/x", "vault:secret/x#", "vault:#field"} {
		if _, ok, err := ParseRef(bad); !ok || err == nil {
			t.Errorf("%q: want malformed-ref error, got ok=%v err=%v", bad, ok, err)
		}
	}
}

func TestResolveKVv1AndKVv2(t *testing.T) {
	fake := newFakeVault()
	fake.token = "s.token"
	fake.set("secret/data/opnsense", map[string]any{"api_key": "k", "api_secret": "s"})
	fake.set("kv/cloudflare", map[string]any{"token": "cf", "port": json.Number("8443"), "on": true})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	c, err := New(Config{Addr: srv.URL, Token: "s.token", Namespace: "team"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Resolve(context.Background(), map[string]Ref{
		"KEY":    {Path: "secret/data/opnsense", Field: "api_key"},
		"SECRET": {Path: "secret/data/opnsense", Field: "api_secret"},
		"CF":     {Path: "kv/cloudflare", Field: "token"},
		"PORT":   {Path: "kv/cloudflare", Field: "port"},
		"ON":     {Path: "kv/cloudflare", Field: "on"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"KEY": "k", "SECRET": "s", "CF": "cf", "PORT": "8443", "ON": "true"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if fake.reads != 2 {
		t.Errorf("reads = %d, want one per path (2)", fake.reads)
	}
	if fake.namespace != "team" {
		t.Errorf("namespace header = %q", fake.namespace)
	}
}

func TestResolveErrors(t *testing.T) {
	fake := newFakeVault()
	fake.token = "right"
	fake.set("kv/app", map[string]any{"obj": map[string]any{}})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	for name, tc := range map[string]struct {
		token string
		ref   Ref
		want  string
	}{
		"denied":        {"wrong", Ref{"kv/app", "obj"}, "permission denied"},
		"missing path":  {"right", Ref{"kv/nope", "x"}, "status 404"},
		"missing field": {"right", Ref{"kv/app", "x"}, `field "x" not found`},
		"not a string":  {"right", Ref{"kv/app", "obj"}, "not a string"},
	} {
		c, err := New(Config{Addr: srv.URL, Token: tc.token})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Resolve(context.Background(), map[string]Ref{"X": tc.ref})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// TestUnixSocketWithoutToken covers a proxy (e.g. Wyrwood's vault.sock) that
// adds the token itself: the client sends none.
func TestUnixSocketWithoutToken(t *testing.T) {
	fake := newFakeVault()
	fake.set("secret/data/app", map[string]any{"password": "p"})
	var sawToken bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawToken = r.Header["X-Vault-Token"]
		fake.ServeHTTP(w, r)
	})

	sock := filepath.Join(t.TempDir(), "vault.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	c, err := New(Config{Addr: "unix://" + sock})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Resolve(context.Background(), map[string]Ref{"P": {Path: "secret/data/app", Field: "password"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["P"] != "p" {
		t.Errorf("P = %q", got["P"])
	}
	if sawToken {
		t.Error("client sent X-Vault-Token with no token configured")
	}
}

func TestTokenFileIsReadPerRequest(t *testing.T) {
	fake := newFakeVault()
	fake.token = "one"
	fake.set("kv/app", map[string]any{"v": "x"})
	srv := httptest.NewServer(fake)
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Addr: srv.URL, TokenFile: file})
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]Ref{"V": {Path: "kv/app", Field: "v"}}
	if _, err := c.Resolve(context.Background(), refs); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.token = "two"
	fake.mu.Unlock()
	if err := os.WriteFile(file, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(context.Background(), refs); err != nil {
		t.Fatalf("rotated token not picked up: %v", err)
	}
}

func TestNewErrors(t *testing.T) {
	for _, addr := range []string{"", "ftp://vault", "unix://"} {
		if _, err := New(Config{Addr: addr}); err == nil {
			t.Errorf("%q: want error", addr)
		}
	}
	if _, err := ConfigFromEnv(func(k string) string {
		if k == "VAULT_SKIP_VERIFY" {
			return "maybe"
		}
		return ""
	}); err == nil {
		t.Error("VAULT_SKIP_VERIFY=maybe: want error")
	}
}
