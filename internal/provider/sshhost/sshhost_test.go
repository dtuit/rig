package sshhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

func driver(t *testing.T, c Config, down ...string) *Driver {
	t.Helper()
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	d.Dial = func(_ context.Context, addr string) error {
		for _, h := range down {
			if strings.HasPrefix(addr, h+":") {
				return errors.New("refused")
			}
		}
		return nil
	}
	return d
}

// The block decodes through the registry the way the CLI opens it, and a
// host with an @ in its user (an AD login) keeps it whole.
func TestFactoryDecodesHosts(t *testing.T) {
	var pc config.ProviderConfig
	src := "user: me@corp.example\nhosts:\n  gpu13:\n    host: gpu13.corp.example\n    gpu: 1x RTX 4090\n"
	if err := yaml.Unmarshal([]byte(src), &pc); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Open(Name, pc)
	if err != nil {
		t.Fatal(err)
	}
	d := p.(*Driver)
	d.Dial = func(context.Context, string) error { return nil }
	got, err := d.ListInstances(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := provider.Instance{
		ID: "gpu13", Name: "rig-gpu13", Type: "ssh", Status: provider.Running, RawStatus: "reachable",
		IP: "gpu13.corp.example", Location: "local", GPU: "1x RTX 4090",
		SSH: provider.Endpoint{Host: "gpu13.corp.example", Port: 22, User: "me@corp.example"},
	}
	if len(got) != 1 || got[0].Name != want.Name || got[0].SSH != want.SSH || got[0].Status != want.Status || got[0].GPU != want.GPU {
		t.Errorf("ListInstances = %+v\nwant [%+v]", got, want)
	}
}

func TestListProbesEachHostAndSortsByName(t *testing.T) {
	d := driver(t, Config{User: "u", Hosts: map[string]Host{
		"b": {Host: "hb", Port: 2222, User: "other"},
		"a": {Host: "ha"},
	}}, "hb")
	got, err := d.ListInstances(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "rig-a" || got[1].Name != "rig-b" {
		t.Fatalf("names = %v, want rig-a, rig-b", got)
	}
	if got[0].Status != provider.Running {
		t.Errorf("reachable host status = %s, want running", got[0].Status)
	}
	if got[1].Status != provider.Error || got[1].RawStatus != "unreachable" {
		t.Errorf("unreachable host = %s/%s, want error/unreachable", got[1].Status, got[1].RawStatus)
	}
	if e := got[1].SSH; e.Port != 2222 || e.User != "other" {
		t.Errorf("per-host port/user = %+v, want 2222 and other", e)
	}
}

func TestNewRejectsIncompleteHosts(t *testing.T) {
	for name, c := range map[string]Config{
		"no host": {User: "u", Hosts: map[string]Host{"a": {}}},
		"no user": {Hosts: map[string]Host{"a": {Host: "h"}}},
		"bad key": {User: "u", Hosts: map[string]Host{"Big_Box": {Host: "h"}}},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%s: New accepted %+v", name, c)
		}
	}
}

func TestLifecycleIsUnsupported(t *testing.T) {
	d := driver(t, Config{User: "u", Hosts: map[string]Host{"a": {Host: "h"}}})
	ctx := t.Context()
	if _, err := d.CreateInstance(ctx, provider.CreateInstanceRequest{}); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("CreateInstance err = %v", err)
	}
	if err := d.DeleteInstance(ctx, "a", nil); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("DeleteInstance err = %v", err)
	}
	if _, err := d.GetInstance(ctx, "nope"); err == nil {
		t.Error("GetInstance of an unconfigured host should fail")
	}
	if !d.Capabilities().Static {
		t.Error("Capabilities().Static = false")
	}
}
