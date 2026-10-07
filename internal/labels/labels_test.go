package labels

import (
	"reflect"
	"strings"
	"testing"
)

func TestHostnames(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		prefix string
		want   []string
	}{
		{"no labels", map[string]string{}, "caddy", []string{}},
		{"single", map[string]string{"caddy": "app.home.arpa"}, "caddy", []string{"app.home.arpa"}},
		{"space and comma separated", map[string]string{"caddy": "a.home.arpa, b.home.arpa c.home.arpa"}, "caddy",
			[]string{"a.home.arpa", "b.home.arpa", "c.home.arpa"}},
		{"numbered site labels", map[string]string{"caddy_0": "a.home.arpa", "caddy_1": "b.home.arpa"}, "caddy",
			[]string{"a.home.arpa", "b.home.arpa"}},
		{"directives are ignored", map[string]string{"caddy": "a.home.arpa", "caddy.reverse_proxy": "{{upstreams 80}}", "caddy_0.tls": "internal"}, "caddy",
			[]string{"a.home.arpa"}},
		{"scheme port and path stripped", map[string]string{"caddy": "https://A.Home.Arpa:8443 http://b.home.arpa/x"}, "caddy",
			[]string{"a.home.arpa", "b.home.arpa"}},
		{"skips unusable addresses", map[string]string{"caddy": "*.home.arpa :80 localhost 10.0.0.5 {$HOST} (snippet) app."}, "caddy",
			[]string{}},
		{"dedupes", map[string]string{"caddy_0": "a.home.arpa", "caddy_1": "a.home.arpa"}, "caddy", []string{"a.home.arpa"}},
		{"custom prefix", map[string]string{"proxy": "a.home.arpa", "caddy": "b.home.arpa"}, "proxy", []string{"a.home.arpa"}},
		{"prefix is not a substring match", map[string]string{"caddyx": "a.home.arpa"}, "caddy", []string{}},
		{"opt out", map[string]string{"caddy": "a.home.arpa", DNSLabel: "false"}, "caddy", nil},
		{"opt out is case insensitive", map[string]string{"caddy": "a.home.arpa", DNSLabel: "FALSE"}, "caddy", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Hostnames(tt.labels, tt.prefix)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Hostnames() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSplitFQDN(t *testing.T) {
	tests := []struct {
		in, host, domain string
		ok               bool
	}{
		{"app.home.arpa", "app", "home.arpa", true},
		{"app.lan", "app", "lan", true},
		{"app", "", "", false},
		{".lan", "", "", false},
		{"app.", "", "", false},
	}
	for _, tt := range tests {
		h, d, ok := SplitFQDN(tt.in)
		if h != tt.host || d != tt.domain || ok != tt.ok {
			t.Errorf("SplitFQDN(%q) = %q, %q, %v", tt.in, h, d, ok)
		}
	}
}

func TestPortForwards(t *testing.T) {
	got, errs := PortForwards(map[string]string{
		"caddy":                             "app.home.arpa",
		"gateway.portforward.1.protocol":    "UDP",
		"gateway.portforward.1.src_port":    "51820",
		"gateway.portforward.1.dst_port":    "51820",
		"gateway.portforward.0.protocol":    "tcp",
		"gateway.portforward.0.src_port":    "2222",
		"gateway.portforward.0.dst_port":    "22",
		"gateway.portforward.0.description": "SSH",
		"gateway.portforward.10.protocol":   "tcp/udp",
		"gateway.portforward.10.src_port":   "27015-27020",
		"gateway.portforward.10.dst_port":   "27015",
	})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := []PortForward{
		{Index: 0, Protocol: "tcp", SrcPort: "2222", DstPort: "22", Description: "SSH"},
		{Index: 1, Protocol: "udp", SrcPort: "51820", DstPort: "51820"},
		{Index: 10, Protocol: "tcp/udp", SrcPort: "27015-27020", DstPort: "27015"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PortForwards() = %#v, want %#v", got, want)
	}
}

func TestPortForwardsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		errSub string
	}{
		{"missing dst_port", map[string]string{"gateway.portforward.0.protocol": "tcp", "gateway.portforward.0.src_port": "1"}, "dst_port is required"},
		{"missing protocol", map[string]string{"gateway.portforward.0.src_port": "1", "gateway.portforward.0.dst_port": "1"}, "protocol is required"},
		{"bad protocol", map[string]string{"gateway.portforward.0.protocol": "icmp", "gateway.portforward.0.src_port": "1", "gateway.portforward.0.dst_port": "1"}, "must be tcp"},
		{"port out of range", map[string]string{"gateway.portforward.0.protocol": "tcp", "gateway.portforward.0.src_port": "70000", "gateway.portforward.0.dst_port": "1"}, "src_port"},
		{"dst range", map[string]string{"gateway.portforward.0.protocol": "tcp", "gateway.portforward.0.src_port": "1", "gateway.portforward.0.dst_port": "1-2"}, "dst_port"},
		{"non numeric index", map[string]string{"gateway.portforward.ssh.protocol": "tcp"}, "expected"},
		{"unknown field", map[string]string{"gateway.portforward.0.proto": "tcp"}, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := PortForwards(tt.labels)
			if len(got) != 0 {
				t.Errorf("expected no valid forwards, got %v", got)
			}
			joined := ""
			for _, e := range errs {
				joined += e.Error() + "\n"
			}
			if !strings.Contains(joined, tt.errSub) {
				t.Errorf("errors %q do not mention %q", joined, tt.errSub)
			}
		})
	}
}
