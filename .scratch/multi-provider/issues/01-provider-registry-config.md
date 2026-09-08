# 01 — Prefactor: provider registry, per-provider config and credentials

**What to build:** rig can be told which cloud to drive — `--provider` on the root command, a
`provider:` pin on a profile, or `provider:` at the top of the config — and resolves that
provider's credentials the way it resolves Verda's today. With no new config, Verda behaves
exactly as before. No new driver ships in this issue; the only registered provider is Verda.

**Blocked by:** None — can start immediately.

**Status:** resolved

- [x] `internal/provider` exposes `Register(name, Factory)` and `Open(name, cfg)`; the Verda
      package registers itself; `cli.newApp` uses `Open` instead of importing `verda` directly.
- [x] Config gains `providers: map[string]ProviderConfig` where `ProviderConfig` holds
      `credentials_file` plus a raw `yaml.Node`/map the driver decodes for its own keys.
      Top-level `credentials_file` still means Verda's.
- [x] Resolution order: `--provider` flag → profile `provider:` → config `provider:` → `verda`.
      `rig up <profile>` honours the profile pin; `--provider` overrides it with a warning.
- [x] `config.Credentials()` is replaced by a per-provider `Creds(name) (map[string]string, error)`:
      the provider's named env vars first, then its `credentials_file`. Verda: `VERDA_CLIENT_ID`,
      `VERDA_CLIENT_SECRET`. Error text names the variables and the file, as today.
- [x] Unknown provider name → `unknown provider %q (have: verda)`; the `rig config` example
      shows a `providers:` block.
- [x] Existing config files load unchanged; existing tests pass; `rig` with the current config
      produces identical output.

## Notes

Spec: ../spec.md — "Registry, not a switch", "Config shape". Keep `ProviderConfig` decoding in
the driver (each driver knows its keys); config only carries the block. Env var acceptance for
RunPod (`RUNPOD_API_KEY` and `RUNPOD_KEY`) and Vast (`VAST_API_KEY`) is declared by those
drivers in 06/08 — design the credential lookup so a driver can list several env names.

## Comments

2026-09-08 (agent): Landed. `internal/provider/registry.go`: `type Factory func(cfg
config.ProviderConfig) (Provider, error)`, `Register(name, f)` (panics on a duplicate name),
`Open(name, cfg)` erroring `unknown provider %q (have: <sorted names>)`, `Names() []string`.
`internal/provider/verda/verda.go`: `init()` registers `verda`, whose factory calls
`cfg.Creds([]string{"VERDA_CLIENT_ID", "VERDA_CLIENT_SECRET"})` then the unchanged `New(id, secret)`.
`internal/config/config.go`: `Profile.Provider` (`provider:` pin), `Config.Providers
map[string]ProviderConfig` (`providers:`), `ProviderConfig{CredentialsFile}` keeping its raw
`yaml.Node` via `UnmarshalYAML`, with `Decode(out any) error` (absent block = no-op) and
`Creds(envNames []string) (map[string]string, error)` (per name: env first, then the file; error
`X/Y not set and no credentials_file in <cfg>` — `providers.<name>.credentials_file` for non-Verda
— or `<file> missing X/Y` listing only the unresolved names). `Config.ProviderConfig(name)` fills
the top-level `credentials_file` in as Verda's fallback only; `Config.Creds(provider, envNames)`
delegates to it. The old `Credentials()` is removed. `internal/cli/root.go`: `--provider` persistent
flag; blank-imports `provider/verda`; `newApp()` delegates to `newAppFor(cfg *config.Config, pin
string)` which resolves `--provider` → pin → `cfg.Provider` → `verda`, logging `rig: --provider X
overrides profile pin Y` when the flag beats a differing pin. `internal/cli/lifecycle.go`: `up
<profile>` loads config and resolves the profile before building the app and passes
`prof.Provider` as the pin. `internal/core/core.go`: `App.Provider() string`.
`internal/cli/inventory.go`: example config gains the `providers:` block (verda, runpod, vast, aws)
and a pinned `pod` profile. Tests: `internal/config/config_test.go` (old-style load, top-level
fallback is Verda-only, providers block wins, env beats file, missing-var/file error text, Decode,
profile pin), `internal/provider/registry_test.go` (unknown-name text, factory receives the block,
sorted Names), `internal/cli/provider_test.go` (flag, config, profile pin, flag-over-pin warning,
unknown provider lists `testfake, verda`). Deviations: no `Env` cache field on `ProviderConfig` —
the file is read on each `Creds` call, which happens once per process; mixed resolution (one name
from env, the other from the file) now succeeds where the old code required both from one source.
Root `Short` still says "(Verda)" — left for the docs issue.
