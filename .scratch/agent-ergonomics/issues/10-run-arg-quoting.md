# 10 — `rig run` loses argument quoting

Status: resolved
Source: agent session d301490d (training run on one H100, 2026-09-06); code read against ec9471b

## Problem

`rig run` joins its arguments with spaces (`internal/cli/jobs.go:43`, `strings.Join(args, " ")`)
and writes the result into the job script. The local shell has already removed the quotes, so a
quoted list argument arrives on the instance as separate words:

```
rig run --name pipeline -- bash pipeline.sh 700 32 "A D E"
# on the instance: bash pipeline.sh 700 32 A D E   -> $3 is "A"
```

In the session the job trained only format A; D and E had to be re-queued as a second job. This is
the third recorded variant of the same fault (`bash -c '...'` fragments, `~` expanding locally, now
quoted lists), all in the user's rig gotchas memory rather than fixed in rig.

## Shape

Shell-quote each argument before joining (single-quote with `'\''` escaping, or a small
`shellQuote` helper), so the job script reproduces the argv the user typed. `--workdir` already
uses `%q`; this is the same instinct applied to the command.

## Where

- `internal/cli/jobs.go:43` (join) and `internal/remote/remote.go` `StartJob` (writes the script).
- `internal/cli/access.go:51` (`rig ssh -- cmd`) has the same join; decide whether it should quote
  too or stay raw so `rig ssh -- 'a && b'` keeps working. Document whichever is chosen.

## Done when

`rig run -- printf '%s\n' "A D E" x` logs two lines, `A D E` and `x`. A test on the quoting
helper covers spaces, single quotes, and empty arguments.

## Comments

**Fixed (2026-09-06, agent):** new package `internal/shellq` — `Quote(s)` single-quotes a
word with `'\''` escaping (plain words unchanged, empty arg becomes `''`) and `Join(args)`
joins the quoted words. `rig run` (`internal/cli/jobs.go`) now writes `shellq.Join(args)`
into the job script, so the job sees the argv the user typed; `StartJob` is unchanged. Tests
in `internal/shellq/shellq_test.go` cover spaces, single quotes, empty, plain words, and run
the done-when `printf '%s\n' "A D E" x` through a real `sh`. `rig ssh -- cmd`
(`internal/cli/access.go`) deliberately keeps the raw join so `rig ssh -- 'a && b'` still
runs a shell expression on the instance; `run` reproduces argv, `ssh` passes a command
string.
