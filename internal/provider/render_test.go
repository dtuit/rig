package provider_test

import (
	"testing"

	"github.com/dtuit/rig/internal/provider"
)

// An endpoint reads as the host alone on the default ssh port — as every VM
// is — and as host:port for a container's mapped sshd.
func TestEndpointString(t *testing.T) {
	cases := []struct {
		name string
		ep   provider.Endpoint
		want string
	}{
		{"no port", provider.Endpoint{Host: "10.0.0.5"}, "10.0.0.5"},
		{"port 22", provider.Endpoint{Host: "10.0.0.5", Port: 22}, "10.0.0.5"},
		{"mapped port", provider.Endpoint{Host: "213.192.2.102", Port: 40085}, "213.192.2.102:40085"},
		{"empty host", provider.Endpoint{Port: 40085}, ":40085"},
		{"empty host, no port", provider.Endpoint{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.ep.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

// Instance.Endpoint is what rig dials: the ssh endpoint, with the display IP
// standing in as the host for a driver that fills none.
func TestInstanceEndpointFallsBackToIP(t *testing.T) {
	in := provider.Instance{IP: "10.0.0.5"}
	if got := in.Endpoint().String(); got != "10.0.0.5" {
		t.Errorf("Endpoint() = %q, want the IP as the host", got)
	}
	in = provider.Instance{IP: "1.2.3.4", SSH: provider.Endpoint{Host: "proxy.vast.ai", Port: 40085}}
	if got := in.Endpoint().String(); got != "proxy.vast.ai:40085" {
		t.Errorf("Endpoint() = %q, want the ssh endpoint, not the IP", got)
	}
}

// A catalogue that does not quote system RAM (RunPod's) reports 0, which
// reads as a dash, not 0GB.
func TestInstanceTypeMemoryText(t *testing.T) {
	if got := (provider.InstanceType{MemoryGB: 80}).MemoryText(); got != "80GB" {
		t.Errorf("MemoryText() = %q, want 80GB", got)
	}
	if got := (provider.InstanceType{}).MemoryText(); got != "-" {
		t.Errorf("MemoryText() = %q, want -", got)
	}
}
