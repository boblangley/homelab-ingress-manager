// Package opnsensetest provides an in-memory fake of the OPNsense API endpoints
// used by homelab-ingress-manager. It mirrors the request and response shapes
// of OPNsense 26.x (see the Unbound and Firewall DNat models in opnsense/core),
// including HTTP 200 responses with result "failed" on validation errors and
// flattened dotted keys in NAT search rows.
package opnsensetest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	// APIKey and APISecret are the credentials the fake server accepts.
	APIKey    = "test-key"
	APISecret = "test-secret"
)

// HostOverride is a stored Unbound host override.
type HostOverride struct {
	UUID        string `json:"uuid"`
	Enabled     string `json:"enabled"`
	Hostname    string `json:"hostname"`
	Domain      string `json:"domain"`
	RR          string `json:"rr"`
	Server      string `json:"server"`
	Description string `json:"description"`
}

// NATRule is a stored destination NAT rule.
type NATRule struct {
	UUID               string
	Disabled           string
	Interface          string
	IPProtocol         string
	Protocol           string
	SourceNetwork      string
	SourcePort         string
	DestinationNetwork string
	DestinationPort    string
	Target             string
	LocalPort          string
	Pass               string
	Description        string
}

// Server is a fake OPNsense instance.
type Server struct {
	*httptest.Server

	mu    sync.Mutex
	seq   int
	hosts []HostOverride
	rules []NATRule
	calls map[string]int
	fail  map[string]int
}

// New starts a fake server that is closed when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{calls: map[string]int{}, fail: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/unbound/settings/search_host_override", s.searchHosts)
	mux.HandleFunc("/api/unbound/settings/add_host_override", s.addHost)
	mux.HandleFunc("/api/unbound/settings/del_host_override/{uuid}", s.delHost)
	mux.HandleFunc("/api/unbound/service/reconfigure", s.status)
	mux.HandleFunc("/api/firewall/d_nat/search_rule", s.searchRules)
	mux.HandleFunc("/api/firewall/d_nat/add_rule", s.addRule)
	mux.HandleFunc("/api/firewall/d_nat/del_rule/{uuid}", s.delRule)
	mux.HandleFunc("/api/firewall/d_nat/apply", s.status)
	s.Server = httptest.NewServer(s.middleware(mux))
	t.Cleanup(s.Close)
	return s
}

// SeedHostOverride stores h as if it had been created through the UI and returns its UUID.
func (s *Server) SeedHostOverride(h HostOverride) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	h.UUID = s.nextUUID()
	s.hosts = append(s.hosts, h)
	return h.UUID
}

// SeedNATRule stores r as if it had been created through the UI and returns its UUID.
func (s *Server) SeedNATRule(r NATRule) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.UUID = s.nextUUID()
	s.rules = append(s.rules, r)
	return r.UUID
}

// HostOverrides returns a copy of the stored host overrides.
func (s *Server) HostOverrides() []HostOverride {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]HostOverride(nil), s.hosts...)
}

// NATRules returns a copy of the stored NAT rules.
func (s *Server) NATRules() []NATRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]NATRule(nil), s.rules...)
}

// Calls returns how many authenticated requests were made to path.
func (s *Server) Calls(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[path]
}

// FailNext makes the next n requests to path return HTTP 500.
func (s *Server) FailNext(path string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[path] = n
}

func (s *Server) nextUUID() string {
	s.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", s.seq)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, secret, ok := r.BasicAuth()
		if !ok || key != APIKey || secret != APISecret {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"status": 401, "message": "Authentication Failed"})
			return
		}
		if r.Method != http.MethodPost && !strings.Contains(r.URL.Path, "/search_") {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		path := r.URL.Path
		if i := strings.Index(path, "/del_"); i >= 0 {
			path = path[:strings.LastIndex(path, "/")]
		}
		s.mu.Lock()
		s.calls[path]++
		failing := s.fail[path] > 0
		if failing {
			s.fail[path]--
		}
		s.mu.Unlock()
		if failing {
			http.Error(w, "injected failure", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func search[T any](w http.ResponseWriter, rows []T) {
	writeJSON(w, map[string]any{"rows": rows, "rowCount": len(rows), "total": len(rows), "current": 1})
}

func failed(w http.ResponseWriter, validations map[string]string) {
	writeJSON(w, map[string]any{"result": "failed", "validations": validations})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) searchHosts(w http.ResponseWriter, _ *http.Request) {
	search(w, s.HostOverrides())
}

var (
	hostnameRe = regexp.MustCompile(`^(\*|[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)$`)
	domainRe   = regexp.MustCompile(`(?i)^(?:(?:[a-z0-9]|[a-z0-9][a-z0-9\-]*[a-z0-9])\.)*(?:[a-z0-9]|[a-z0-9][a-z0-9\-]*[a-z0-9])$`)
)

func (s *Server) addHost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host HostOverride `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h := body.Host
	v := map[string]string{}
	if h.Hostname != "" && !hostnameRe.MatchString(strings.ToLower(h.Hostname)) {
		v["host.hostname"] = "A valid hostname must be specified."
	}
	if !domainRe.MatchString(h.Domain) {
		v["host.domain"] = "A valid domain must be specified."
	}
	switch h.RR {
	case "A", "AAAA":
		ip := net.ParseIP(h.Server)
		if ip == nil || (h.RR == "A") != (ip.To4() != nil) {
			v["host.server"] = "The field IP address is required."
		}
	default:
		v["host.rr"] = "Option not in list."
	}
	if len(v) > 0 {
		failed(w, v)
		return
	}
	writeJSON(w, map[string]string{"result": "saved", "uuid": s.SeedHostOverride(h)})
}

func (s *Server) delHost(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, h := range s.hosts {
		if h.UUID == uuid {
			s.hosts = append(s.hosts[:i], s.hosts[i+1:]...)
			writeJSON(w, map[string]string{"result": "deleted"})
			return
		}
	}
	writeJSON(w, map[string]string{"result": "not found"})
}

func (s *Server) searchRules(w http.ResponseWriter, _ *http.Request) {
	rows := []map[string]any{}
	for _, r := range s.NATRules() {
		rows = append(rows, map[string]any{
			"uuid":                r.UUID,
			"disabled":            r.Disabled,
			"interface":           r.Interface,
			"ipprotocol":          r.IPProtocol,
			"protocol":            r.Protocol,
			"source.network":      r.SourceNetwork,
			"source.port":         r.SourcePort,
			"destination.network": r.DestinationNetwork,
			"destination.port":    r.DestinationPort,
			"target":              r.Target,
			"local-port":          r.LocalPort,
			"pass":                r.Pass,
			"descr":               r.Description,
			"sort_order":          "400000.0000001",
		})
	}
	// OPNsense appends automatic rules (e.g. anti-lockout) to the result.
	rows = append(rows, map[string]any{
		"uuid":         "automatic_lockout_1",
		"interface":    "lan",
		"protocol":     "tcp",
		"descr":        "Anti-Lockout Rule",
		"is_automatic": true,
	})
	search(w, rows)
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

func validPortOrRange(p string) bool {
	lo, hi, isRange := strings.Cut(p, "-")
	if !isRange {
		return validPort(p)
	}
	return validPort(lo) && validPort(hi)
}

func (s *Server) addRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rule struct {
			Disabled    string `json:"disabled"`
			Interface   string `json:"interface"`
			IPProtocol  string `json:"ipprotocol"`
			Protocol    string `json:"protocol"`
			Source      struct{ Network, Port string }
			Destination struct{ Network, Port string }
			Target      string `json:"target"`
			LocalPort   string `json:"local-port"`
			Pass        string `json:"pass"`
			Descr       string `json:"descr"`
		} `json:"rule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rule := body.Rule
	v := map[string]string{}
	if rule.Interface == "" {
		v["rule.interface"] = "Interface is required."
	}
	switch rule.IPProtocol {
	case "", "inet", "inet6", "inet46":
	default:
		v["rule.ipprotocol"] = "Option not in list."
	}
	switch rule.Protocol {
	case "", "tcp", "udp", "tcp/udp":
	default:
		v["rule.protocol"] = "Option not in list."
	}
	if rule.Destination.Port != "" && !validPortOrRange(rule.Destination.Port) {
		v["rule.destination.port"] = "Please specify a valid port number, well-known name, alias or range."
	}
	if net.ParseIP(rule.Target) == nil {
		v["rule.target"] = "Please specify a valid address or alias."
	}
	if rule.LocalPort != "" && !validPort(rule.LocalPort) {
		v["rule.local-port"] = "Please specify a valid port number or alias."
	}
	switch rule.Pass {
	case "", "pass", "rule":
	default:
		v["rule.pass"] = "Option not in list."
	}
	if len(v) > 0 {
		failed(w, v)
		return
	}
	uuid := s.SeedNATRule(NATRule{
		Disabled:           rule.Disabled,
		Interface:          rule.Interface,
		IPProtocol:         rule.IPProtocol,
		Protocol:           rule.Protocol,
		SourceNetwork:      rule.Source.Network,
		SourcePort:         rule.Source.Port,
		DestinationNetwork: rule.Destination.Network,
		DestinationPort:    rule.Destination.Port,
		Target:             rule.Target,
		LocalPort:          rule.LocalPort,
		Pass:               rule.Pass,
		Description:        rule.Descr,
	})
	writeJSON(w, map[string]string{"result": "saved", "uuid": uuid})
}

func (s *Server) delRule(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, rule := range s.rules {
		if rule.UUID == uuid {
			s.rules = append(s.rules[:i], s.rules[i+1:]...)
			writeJSON(w, map[string]string{"result": "deleted"})
			return
		}
	}
	writeJSON(w, map[string]string{"result": "not found"})
}
