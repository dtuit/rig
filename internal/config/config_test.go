package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadFrom writes text as the config file and loads it, with the Verda env
// vars cleared so only the file and the test's own env are consulted.
func loadFrom(t *testing.T, text string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIG_CONFIG", path)
	t.Setenv("VERDA_CLIENT_ID", "")
	t.Setenv("VERDA_CLIENT_SECRET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func writeEnv(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "creds.env")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var verdaNames = []string{"VERDA_CLIENT_ID", "VERDA_CLIENT_SECRET"}

func TestOldStyleConfigLoadsAsVerda(t *testing.T) {
	cfg := loadFrom(t, "credentials_file: ~/secrets/verda.env\nprofiles:\n  train:\n    types: [1H100.80S.32V]\n")
	if cfg.Provider != "verda" {
		t.Errorf("Provider = %q, want verda", cfg.Provider)
	}
	if cfg.CredentialsFile != "~/secrets/verda.env" {
		t.Errorf("CredentialsFile = %q", cfg.CredentialsFile)
	}
	p, err := cfg.Profile("train")
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "" {
		t.Errorf("profile Provider = %q, want empty (no pin)", p.Provider)
	}
}

func TestCredsFromTopLevelFileFallsBackForVerdaOnly(t *testing.T) {
	file := writeEnv(t, "export VERDA_CLIENT_ID=id1\nVERDA_CLIENT_SECRET=\"sec1\"\n")
	cfg := loadFrom(t, "credentials_file: "+file+"\n")
	got, err := cfg.Creds("verda", verdaNames)
	if err != nil {
		t.Fatal(err)
	}
	if got["VERDA_CLIENT_ID"] != "id1" || got["VERDA_CLIENT_SECRET"] != "sec1" {
		t.Errorf("creds = %v", got)
	}
	_, err = cfg.Creds("runpod", []string{"RUNPOD_API_KEY"})
	if err == nil || !strings.Contains(err.Error(), "RUNPOD_API_KEY not set") {
		t.Errorf("runpod must not inherit the top-level file, got %v", err)
	}
}

func TestCredsFromProvidersBlock(t *testing.T) {
	file := writeEnv(t, "VERDA_CLIENT_ID=id2\nVERDA_CLIENT_SECRET=sec2\n")
	cfg := loadFrom(t, "credentials_file: /nonexistent/verda.env\nproviders:\n  verda:\n    credentials_file: "+file+"\n")
	got, err := cfg.Creds("verda", verdaNames)
	if err != nil {
		t.Fatal(err)
	}
	if got["VERDA_CLIENT_ID"] != "id2" {
		t.Errorf("providers.verda.credentials_file must win over top-level, got %v", got)
	}
}

func TestCredsEnvBeatsFile(t *testing.T) {
	file := writeEnv(t, "VERDA_CLIENT_ID=fileid\nVERDA_CLIENT_SECRET=filesec\n")
	cfg := loadFrom(t, "credentials_file: "+file+"\n")
	t.Setenv("VERDA_CLIENT_ID", "envid")
	got, err := cfg.Creds("verda", verdaNames)
	if err != nil {
		t.Fatal(err)
	}
	if got["VERDA_CLIENT_ID"] != "envid" || got["VERDA_CLIENT_SECRET"] != "filesec" {
		t.Errorf("creds = %v, want env id + file secret", got)
	}
}

func TestCredsMissingNamesVariableAndFile(t *testing.T) {
	file := writeEnv(t, "VERDA_CLIENT_ID=onlyid\n")
	cfg := loadFrom(t, "credentials_file: "+file+"\n")
	_, err := cfg.Creds("verda", verdaNames)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "VERDA_CLIENT_SECRET") || strings.Contains(err.Error(), "VERDA_CLIENT_ID") {
		t.Errorf("error must name only the missing variable: %v", err)
	}
	if !strings.Contains(err.Error(), file) {
		t.Errorf("error must name the file consulted: %v", err)
	}
}

func TestCredsNoFileConfigured(t *testing.T) {
	cfg := loadFrom(t, "provider: verda\n")
	_, err := cfg.Creds("verda", verdaNames)
	want := "VERDA_CLIENT_ID/VERDA_CLIENT_SECRET not set and no credentials_file in " + cfg.Path()
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}

func TestProviderConfigDecodesDriverKeys(t *testing.T) {
	cfg := loadFrom(t, "providers:\n  runpod:\n    credentials_file: /x/runpod.env\n    cloud: community\n")
	pc := cfg.ProviderConfig("runpod")
	if pc.CredentialsFile != "/x/runpod.env" {
		t.Errorf("CredentialsFile = %q", pc.CredentialsFile)
	}
	var own struct {
		Cloud string `yaml:"cloud"`
	}
	if err := pc.Decode(&own); err != nil {
		t.Fatal(err)
	}
	if own.Cloud != "community" {
		t.Errorf("cloud = %q, want community", own.Cloud)
	}
	var empty struct {
		Cloud string `yaml:"cloud"`
	}
	if err := cfg.ProviderConfig("vast").Decode(&empty); err != nil || empty.Cloud != "" {
		t.Errorf("absent block must decode to zero value, got %v %v", empty, err)
	}
}

func TestProfileProviderPin(t *testing.T) {
	cfg := loadFrom(t, "profiles:\n  cheap:\n    provider: runpod\n    types: [1xA100-SXM]\n")
	p, err := cfg.Profile("cheap")
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "runpod" {
		t.Errorf("Provider = %q, want runpod", p.Provider)
	}
}
