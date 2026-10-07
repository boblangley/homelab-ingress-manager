// Package labels extracts gateway intent from Docker container labels.
//
// Host names come from the same labels caddy-docker-proxy uses to build site
// blocks (caddy, caddy_0, caddy_1, ...). Port forwards and opt-outs use the
// gateway.* namespace:
//
//	gateway.dns=false                         skip DNS overrides for this container
//	gateway.portforward.<n>.protocol=tcp      tcp, udp or tcp/udp
//	gateway.portforward.<n>.src_port=2222     port (or range) on the WAN side
//	gateway.portforward.<n>.dst_port=22       port on the Docker host
//	gateway.portforward.<n>.description=SSH   optional free text
package labels

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// DNSLabel disables DNS overrides for a container when set to "false".
	DNSLabel = "gateway.dns"
	// PortForwardPrefix starts every port forward label.
	PortForwardPrefix = "gateway.portforward."
)

// Hostnames returns the DNS names served by a container, taken from its
// caddy-docker-proxy site labels (prefix, prefix_0, prefix_1, ...). Wildcards,
// IP addresses, placeholders, snippets and single-label names are skipped
// because they cannot become host overrides. The result is sorted and unique.
func Hostnames(labels map[string]string, prefix string) []string {
	if strings.EqualFold(labels[DNSLabel], "false") {
		return nil
	}
	siteKey := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `(_\d+)?$`)
	seen := map[string]bool{}
	for key, value := range labels {
		if !siteKey.MatchString(key) {
			continue
		}
		for _, addr := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			if host := hostFromAddress(addr); host != "" {
				seen[host] = true
			}
		}
	}
	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// hostFromAddress extracts a DNS name from a Caddy site address such as
// "app.home.arpa", "https://app.home.arpa:8443" or "app.home.arpa/path".
func hostFromAddress(addr string) string {
	if strings.ContainsAny(addr, "{}()*") {
		return ""
	}
	if !strings.Contains(addr, "://") {
		addr = "placeholder://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	if i := strings.IndexByte(host, '.'); i < 1 || i == len(host)-1 {
		return ""
	}
	return host
}

// SplitFQDN splits "app.home.arpa" into ("app", "home.arpa").
func SplitFQDN(fqdn string) (hostname, domain string, ok bool) {
	hostname, domain, found := strings.Cut(fqdn, ".")
	if !found || hostname == "" || domain == "" {
		return "", "", false
	}
	return hostname, domain, true
}

// PortForward is a single gateway.portforward.<n>.* group.
type PortForward struct {
	Index       int
	Protocol    string
	SrcPort     string
	DstPort     string
	Description string
}

// PortForwards parses gateway.portforward.<n>.* labels, ordered by index.
// Incomplete or invalid groups are returned as errors and left out of the result.
func PortForwards(labels map[string]string) ([]PortForward, []error) {
	groups := map[int]*PortForward{}
	var errs []error
	for key, value := range labels {
		rest, ok := strings.CutPrefix(key, PortForwardPrefix)
		if !ok {
			continue
		}
		idxStr, field, ok := strings.Cut(rest, ".")
		idx, err := strconv.Atoi(idxStr)
		if !ok || err != nil || idx < 0 {
			errs = append(errs, fmt.Errorf("label %s: expected %s<n>.<field>", key, PortForwardPrefix))
			continue
		}
		pf := groups[idx]
		if pf == nil {
			pf = &PortForward{Index: idx}
			groups[idx] = pf
		}
		value = strings.TrimSpace(value)
		switch field {
		case "protocol":
			pf.Protocol = strings.ToLower(value)
		case "src_port":
			pf.SrcPort = value
		case "dst_port":
			pf.DstPort = value
		case "description":
			pf.Description = value
		default:
			errs = append(errs, fmt.Errorf("label %s: unknown field %q", key, field))
		}
	}

	indices := make([]int, 0, len(groups))
	for idx := range groups {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	var result []PortForward
	for _, idx := range indices {
		pf := groups[idx]
		if err := pf.validate(); err != nil {
			errs = append(errs, fmt.Errorf("%s%d: %w", PortForwardPrefix, idx, err))
			continue
		}
		result = append(result, *pf)
	}
	return result, errs
}

func (pf PortForward) validate() error {
	switch pf.Protocol {
	case "tcp", "udp", "tcp/udp":
	case "":
		return fmt.Errorf("protocol is required")
	default:
		return fmt.Errorf("protocol %q must be tcp, udp or tcp/udp", pf.Protocol)
	}
	if pf.SrcPort == "" {
		return fmt.Errorf("src_port is required")
	}
	if !validPortOrRange(pf.SrcPort) {
		return fmt.Errorf("src_port %q is not a port or range", pf.SrcPort)
	}
	if pf.DstPort == "" {
		return fmt.Errorf("dst_port is required")
	}
	if !validPort(pf.DstPort) {
		return fmt.Errorf("dst_port %q is not a port", pf.DstPort)
	}
	return nil
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
