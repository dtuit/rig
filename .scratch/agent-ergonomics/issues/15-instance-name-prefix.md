# 15 — `--name rig-x` yields `rig-rig-x`, and commands print different forms of the name

Status: resolved
Source: agent session d301490d (training run on one H100, 2026-09-06); code read against ec9471b

## Problem

A handoff document told the agent to run `rig up --name rig-exp42b`. Rig prefixes every name
with `rig-` (`internal/core/core.go:239`), so the instance became `rig-rig-exp42b`. The
agent then had to learn that `-i rig-exp42b` resolves (Resolve accepts both forms) while the
printed name does not match `rig status`.

Inconsistent rendering: `rig up` (`internal/cli/lifecycle.go:90`) and `rig down` print the full
hostname; `rig status` prints the trimmed name; the ledger and `rig spend` store the full one.

## Shape

- In `Up`, if `spec.Name` already starts with `rig-`, either strip it with a stderr note or refuse
  with "name already carries the rig- prefix". Stripping is friendlier for agents.
- Pick one display form (the short name, as `status` does) and use it in `up`, `down`, `run`,
  and error messages; keep the full hostname in `--json` since that is the provider's field.

## Where

`internal/core/core.go` `Up`, `internal/cli/lifecycle.go` up/down output, `internal/cli/jobs.go`
("started on %s").

## Comments

**Prefix stripped, short name everywhere in lifecycle (2026-09-06, agent):** `core.Up` strips a
leading `rig-` from `spec.Name` and logs `--name already carries the rig- prefix; using <short>`
on stderr, so `rig up --name rig-exp42b` creates `rig-exp42b`. Display form is the short
name, as `rig status` already had it: `rig up` ("<short> is up: ..."), `rig down` (confirm
prompt, "deleted" line, error prefix), `rig extend`, `rig adopt`, and the `rig sync` header
(`internal/cli/lifecycle.go`, `adopt.go`, `access.go`). `--json` output and the ledger keep the
full hostname (it is the provider's field). `internal/cli/jobs.go` ("started on %s") was not
this change's file — still prints whatever it did.
**jobs.go half (2026-09-06, agent):** `internal/cli/jobs.go` now prints the short instance name
(`strings.TrimPrefix(in.Name, core.HostPrefix)`) in `job "x" started on <name>`, matching
`rig status`. The `Up` prefix handling and `up`/`down` output live in `core.go` and
`lifecycle.go` and are left for that change set; status stays open until then.
