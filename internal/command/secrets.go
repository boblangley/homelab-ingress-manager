package command

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/boblangley/homelab-ingress-manager/internal/vault"
)

const defaultVaultRefresh = 5 * time.Minute

// secrets resolves environment variables whose value is a Vault reference
// ("vault:<path>#<field>") and keeps them current.
type secrets struct {
	client   *vault.Client
	refs     map[string]vault.Ref
	values   map[string]string
	interval time.Duration
	setenv   func(key, value string) error
	log      *zap.Logger
}

// vaultRefs returns the environment variables (KEY=VALUE pairs) whose value
// is a Vault reference. VAULT_* variables configure the client and are never
// treated as references.
func vaultRefs(environ []string) (map[string]vault.Ref, error) {
	refs := map[string]vault.Ref{}
	for _, kv := range environ {
		key, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "VAULT_") {
			continue
		}
		ref, ok, err := vault.ParseRef(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if ok {
			refs[key] = ref
		}
	}
	return refs, nil
}

// loadSecrets resolves every Vault reference in environ and writes the values
// back with setenv. It returns nil when there are no references.
func loadSecrets(ctx context.Context, environ []string, getenv func(string) string, setenv func(string, string) error, log *zap.Logger) (*secrets, error) {
	refs, err := vaultRefs(environ)
	if err != nil || len(refs) == 0 {
		return nil, err
	}
	cfg, err := vault.ConfigFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	client, err := vault.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("vault references found (%s): %w", strings.Join(slices.Sorted(maps.Keys(refs)), ", "), err)
	}
	interval := defaultVaultRefresh
	if v := getenv("INGRESS_VAULT_REFRESH_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("INGRESS_VAULT_REFRESH_INTERVAL: invalid duration %q", v)
		}
		interval = d
	}
	s := &secrets{client: client, refs: refs, values: map[string]string{}, interval: interval, setenv: setenv, log: log}
	if _, err := s.refresh(ctx); err != nil {
		return nil, err
	}
	log.Info("resolved secrets from Vault",
		zap.String("addr", cfg.Addr),
		zap.Strings("vars", slices.Sorted(maps.Keys(refs))),
		zap.Duration("refreshInterval", interval))
	return s, nil
}

// refresh reads every reference and sets the variables whose value changed.
// On error nothing is changed.
func (s *secrets) refresh(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	values, err := s.client.Resolve(ctx, s.refs)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	var changed []string
	for key, value := range values {
		if old, ok := s.values[key]; ok && old == value {
			continue
		}
		if err := s.setenv(key, value); err != nil {
			return changed, fmt.Errorf("set %s: %w", key, err)
		}
		s.values[key] = value
		changed = append(changed, key)
	}
	slices.Sort(changed)
	return changed, nil
}

// run refreshes on every interval until ctx is done and calls onChange with
// the names of the variables that changed. A failed refresh keeps the
// current values.
func (s *secrets) run(ctx context.Context, onChange func(ctx context.Context, changed []string)) {
	if s.interval == 0 {
		return
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		changed, err := s.refresh(ctx)
		if err != nil {
			s.log.Warn("Vault refresh failed; keeping current secrets", zap.Error(err))
		}
		if len(changed) > 0 {
			s.log.Info("secrets changed in Vault", zap.Strings("vars", changed))
			onChange(ctx, changed)
		}
	}
}
