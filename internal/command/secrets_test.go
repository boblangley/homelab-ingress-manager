package command

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// kvServer is a minimal KV v2 Vault API.
type kvServer struct {
	mu      sync.Mutex
	secrets map[string]map[string]any
	fail    bool
}

func (s *kvServer) set(path string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[path] = data
}

func (s *kvServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.secrets[strings.TrimPrefix(r.URL.Path, "/v1/")]
	if s.fail || !ok {
		http.Error(w, `{"errors":["nope"]}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": data, "metadata": map[string]any{}}})
}

func newKVServer(t *testing.T) (*kvServer, string) {
	kv := &kvServer{secrets: map[string]map[string]any{}}
	srv := httptest.NewServer(kv)
	t.Cleanup(srv.Close)
	return kv, srv.URL
}

// envMap is a fake process environment.
type envMap map[string]string

func (e envMap) environ() []string {
	var out []string
	for k, v := range e {
		out = append(out, k+"="+v)
	}
	return out
}
func (e envMap) setenv(k, v string) error { e[k] = v; return nil }

func TestVaultRefs(t *testing.T) {
	refs, err := vaultRefs([]string{
		"PLAIN=value",
		"CLOUDFLARE_API_TOKEN=vault:secret/data/cloudflare#api_token",
		"VAULT_TOKEN=vault:ignored#x",
		"EMPTY=",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs["CLOUDFLARE_API_TOKEN"].Field != "api_token" {
		t.Fatalf("refs = %+v", refs)
	}
	if _, err := vaultRefs([]string{"BAD=vault:no-field"}); err == nil || !strings.Contains(err.Error(), "BAD") {
		t.Fatalf("malformed ref: err = %v", err)
	}
}

func TestLoadSecretsNoRefs(t *testing.T) {
	e := envMap{"OPNSENSE_API_KEY": "plain"}
	s, err := loadSecrets(context.Background(), e.environ(), env(e), e.setenv, zap.NewNop())
	if s != nil || err != nil {
		t.Fatalf("got %v, %v; want nil, nil without VAULT_ADDR", s, err)
	}
}

func TestLoadSecrets(t *testing.T) {
	kv, addr := newKVServer(t)
	kv.set("secret/data/opnsense", map[string]any{"key": "k1", "secret": "s1"})
	e := envMap{
		"VAULT_ADDR":          addr,
		"OPNSENSE_API_KEY":    "vault:secret/data/opnsense#key",
		"OPNSENSE_API_SECRET": "vault:secret/data/opnsense#secret",
		"OPNSENSE_URL":        "https://192.168.1.1",
	}
	s, err := loadSecrets(context.Background(), e.environ(), env(e), e.setenv, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if e["OPNSENSE_API_KEY"] != "k1" || e["OPNSENSE_API_SECRET"] != "s1" || e["OPNSENSE_URL"] != "https://192.168.1.1" {
		t.Fatalf("env after load: %v", e)
	}
	if s.interval != defaultVaultRefresh {
		t.Errorf("interval = %v", s.interval)
	}

	// Unchanged values are not reported.
	if changed, err := s.refresh(context.Background()); err != nil || len(changed) != 0 {
		t.Fatalf("refresh = %v, %v; want no change", changed, err)
	}
	// A rotated secret is.
	kv.set("secret/data/opnsense", map[string]any{"key": "k1", "secret": "s2"})
	changed, err := s.refresh(context.Background())
	if err != nil || !reflect.DeepEqual(changed, []string{"OPNSENSE_API_SECRET"}) || e["OPNSENSE_API_SECRET"] != "s2" {
		t.Fatalf("refresh = %v, %v, env %v", changed, err, e)
	}
	// A failed refresh changes nothing.
	kv.mu.Lock()
	kv.fail = true
	kv.mu.Unlock()
	if _, err := s.refresh(context.Background()); err == nil {
		t.Fatal("want error from failing Vault")
	}
	if e["OPNSENSE_API_SECRET"] != "s2" {
		t.Fatalf("value changed by failed refresh: %q", e["OPNSENSE_API_SECRET"])
	}
}

func TestLoadSecretsErrors(t *testing.T) {
	kv, addr := newKVServer(t)
	kv.set("secret/data/app", map[string]any{"x": "y"})
	for name, e := range map[string]envMap{
		"no VAULT_ADDR":    {"A": "vault:secret/data/app#x"},
		"missing field":    {"VAULT_ADDR": addr, "A": "vault:secret/data/app#nope"},
		"bad interval":     {"VAULT_ADDR": addr, "A": "vault:secret/data/app#x", "INGRESS_VAULT_REFRESH_INTERVAL": "soon"},
		"malformed ref":    {"VAULT_ADDR": addr, "A": "vault:secret/data/app"},
		"negative refresh": {"VAULT_ADDR": addr, "A": "vault:secret/data/app#x", "INGRESS_VAULT_REFRESH_INTERVAL": "-1s"},
	} {
		if _, err := loadSecrets(context.Background(), e.environ(), env(e), e.setenv, zap.NewNop()); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSecretsRunCallsOnChange(t *testing.T) {
	kv, addr := newKVServer(t)
	kv.set("secret/data/app", map[string]any{"token": "one"})
	var mu sync.Mutex
	e := envMap{"VAULT_ADDR": addr, "TOKEN": "vault:secret/data/app#token", "INGRESS_VAULT_REFRESH_INTERVAL": "10ms"}
	setenv := func(k, v string) error { mu.Lock(); defer mu.Unlock(); return e.setenv(k, v) }
	s, err := loadSecrets(context.Background(), e.environ(), env(e), setenv, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	kv.set("secret/data/app", map[string]any{"token": "two"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan []string, 1)
	go s.run(ctx, func(_ context.Context, changed []string) {
		select {
		case got <- changed:
		default:
		}
	})
	select {
	case changed := <-got:
		mu.Lock()
		defer mu.Unlock()
		if !reflect.DeepEqual(changed, []string{"TOKEN"}) || e["TOKEN"] != "two" {
			t.Fatalf("changed = %v, TOKEN = %q", changed, e["TOKEN"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onChange not called")
	}
}
