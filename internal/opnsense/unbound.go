package opnsense

import (
	"context"
	"fmt"
)

// HostOverride is an Unbound DNS host override
// (Services > Unbound DNS > Overrides > Hosts).
type HostOverride struct {
	UUID        string `json:"uuid,omitempty"`
	Enabled     string `json:"enabled"`
	Hostname    string `json:"hostname"`
	Domain      string `json:"domain"`
	RR          string `json:"rr"`
	Server      string `json:"server"`
	Description string `json:"description"`
}

// FQDN returns hostname.domain.
func (h HostOverride) FQDN() string {
	if h.Hostname == "" {
		return h.Domain
	}
	return h.Hostname + "." + h.Domain
}

// SearchHostOverrides returns all Unbound host overrides.
func (c *Client) SearchHostOverrides(ctx context.Context) ([]HostOverride, error) {
	var res struct {
		Rows []HostOverride `json:"rows"`
	}
	if err := c.post(ctx, "/api/unbound/settings/search_host_override", searchRequest, &res); err != nil {
		return nil, fmt.Errorf("search host overrides: %w", err)
	}
	return res.Rows, nil
}

// AddHostOverride creates a host override and returns its UUID.
func (c *Client) AddHostOverride(ctx context.Context, h HostOverride) (string, error) {
	h.UUID = ""
	uuid, err := c.mutate(ctx, "/api/unbound/settings/add_host_override", map[string]any{"host": h})
	if err != nil {
		return "", fmt.Errorf("add host override %s: %w", h.FQDN(), err)
	}
	return uuid, nil
}

// DelHostOverride deletes the host override with the given UUID.
func (c *Client) DelHostOverride(ctx context.Context, uuid string) error {
	if _, err := c.mutate(ctx, "/api/unbound/settings/del_host_override/"+uuid, nil); err != nil {
		return fmt.Errorf("delete host override %s: %w", uuid, err)
	}
	return nil
}

// ReconfigureUnbound applies pending Unbound configuration changes.
func (c *Client) ReconfigureUnbound(ctx context.Context) error {
	if err := c.post(ctx, "/api/unbound/service/reconfigure", nil, nil); err != nil {
		return fmt.Errorf("reconfigure unbound: %w", err)
	}
	return nil
}
