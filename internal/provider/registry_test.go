package provider_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
	"github.com/dtuit/rig/internal/provider/fake"
)

// testDriver decodes a single driver key from its block, the way a real
// driver's Factory does.
type testDriver struct {
	Cloud string `yaml:"cloud"`
}

func init() {
	provider.Register("testfake", func(cfg config.ProviderConfig) (provider.Provider, error) {
		var own testDriver
		if err := cfg.Decode(&own); err != nil {
			return nil, err
		}
		return &fake.Provider{Avail: map[string][]string{"block": {own.Cloud}}}, nil
	})
}

func TestOpenUnknownProviderListsRegistered(t *testing.T) {
	_, err := provider.Open("nope", config.ProviderConfig{})
	want := `unknown provider "nope" (have: testfake)`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}

func TestOpenHandsFactoryTheBlock(t *testing.T) {
	var pc config.ProviderConfig
	if err := yaml.Unmarshal([]byte("credentials_file: /x.env\ncloud: community\n"), &pc); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Open("testfake", pc)
	if err != nil {
		t.Fatal(err)
	}
	avail, _ := p.Availability(t.Context(), false)
	if got := avail["block"]; len(got) != 1 || got[0] != "community" {
		t.Errorf("factory saw block %v, want [community]", got)
	}
}

func TestNamesSorted(t *testing.T) {
	names := provider.Names()
	if len(names) != 1 || names[0] != "testfake" {
		t.Errorf("Names = %v, want [testfake]", names)
	}
}
