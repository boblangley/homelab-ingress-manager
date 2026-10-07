package command

import (
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

var defaultFlags = flagValues{mode: "standalone", labelPrefix: "caddy"}

func TestLoadSettingsDisabled(t *testing.T) {
	s, err := loadSettings(env(nil), defaultFlags)
	if err != nil || s.Enabled() {
		t.Fatalf("expected disabled without error, got %v %v", s.Enabled(), err)
	}
}

func TestLoadSettings(t *testing.T) {
	s, err := loadSettings(env(map[string]string{
		"OPNSENSE_URL":                  "https://192.168.1.1",
		"OPNSENSE_API_KEY":              "k",
		"OPNSENSE_API_SECRET":           "s",
		"OPNSENSE_INSECURE_SKIP_VERIFY": "true",
		"INGRESS_HOST_IP":               "192.168.1.10",
		"INGRESS_INSTANCE":              "edge",
		"INGRESS_RESYNC_INTERVAL":       "1m",
		"CADDY_DOCKER_LABEL_PREFIX":     "proxy",
		"CADDY_DOCKER_SOCKETS":          "unix:///var/run/docker.sock,tcp://10.0.0.2:2375",
	}), flagValues{mode: "controller", labelPrefix: "caddy", dockerSockets: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled() || !s.OPNsense.InsecureSkipVerify || s.Mode != "controller" {
		t.Errorf("unexpected settings %+v", s)
	}
	if s.Reconciler.LabelPrefix != "proxy" || s.Reconciler.DNSTarget != "192.168.1.10" ||
		s.Reconciler.Instance != "edge" || s.Reconciler.WANInterface != "wan" || s.Reconciler.ResyncInterval != time.Minute {
		t.Errorf("unexpected reconciler config %+v", s.Reconciler)
	}
	if !reflect.DeepEqual(s.DockerSocket, []string{"unix:///var/run/docker.sock", "tcp://10.0.0.2:2375"}) {
		t.Errorf("unexpected sockets %v", s.DockerSocket)
	}
}

func TestLoadSettingsDNSTargetOverride(t *testing.T) {
	s, err := loadSettings(env(map[string]string{
		"OPNSENSE_URL": "https://fw", "OPNSENSE_API_KEY": "k", "OPNSENSE_API_SECRET": "s",
		"INGRESS_HOST_IP": "192.168.1.10", "INGRESS_DNS_TARGET": "192.168.1.11",
	}), defaultFlags)
	if err != nil || s.Reconciler.DNSTarget != "192.168.1.11" || s.HostIP != "192.168.1.10" {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestLoadSettingsErrors(t *testing.T) {
	base := map[string]string{"OPNSENSE_URL": "https://fw", "OPNSENSE_API_KEY": "k", "OPNSENSE_API_SECRET": "s"}
	tests := map[string]map[string]string{
		"OPNSENSE_API_SECRET":           {"OPNSENSE_API_SECRET": ""},
		"OPNSENSE_URL":                  {"OPNSENSE_URL": "fw"},
		"INGRESS_HOST_IP":               {"INGRESS_HOST_IP": "nas.lan"},
		"OPNSENSE_INSECURE_SKIP_VERIFY": {"OPNSENSE_INSECURE_SKIP_VERIFY": "maybe"},
		"INGRESS_RESYNC_INTERVAL":       {"INGRESS_RESYNC_INTERVAL": "-1s"},
	}
	for want, override := range tests {
		vars := map[string]string{}
		for k, v := range base {
			vars[k] = v
		}
		for k, v := range override {
			vars[k] = v
		}
		_, err := loadSettings(env(vars), defaultFlags)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: expected error mentioning it, got %v", want, err)
		}
	}
}

func TestHostIPForSocket(t *testing.T) {
	lookup := func(host string) ([]net.IP, error) {
		if host == "nas.lan" {
			return []net.IP{net.ParseIP("fd00::20"), net.ParseIP("192.168.1.20")}, nil
		}
		return nil, errors.New("no such host")
	}
	tests := []struct {
		socket, want string
		wantErr      bool
	}{
		{"", "192.168.1.10", false},
		{"unix:///var/run/docker.sock", "192.168.1.10", false},
		{"tcp://127.0.0.1:2375", "192.168.1.10", false},
		{"tcp://192.168.1.30:2375", "192.168.1.30", false},
		{"https://nas.lan:2376", "192.168.1.20", false},
		{"tcp://missing.lan:2375", "", true},
	}
	for _, tt := range tests {
		got, err := hostIPForSocket(tt.socket, "192.168.1.10", lookup)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("hostIPForSocket(%q) = %q, %v", tt.socket, got, err)
		}
	}
}
