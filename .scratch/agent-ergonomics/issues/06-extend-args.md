# 06 — `rig extend` argument shape is inconsistent

Status: resolved
Source: ../HANDOFF.md §06

## Problem

`down`, `logs`, `stop` take their subject positionally, so `rig extend grid422 8h` reads natural —
and fails with `accepts at most 1 arg(s), received 2`. Real form is `rig extend grid422 --by 8h`.

## Shape

Accept an optional positional duration as a synonym for `--by`, or fail with a message that names
`--by` instead of the generic cobra arity error. Low effort.

## Where

`internal/cli/lifecycle.go:175`.

## Follow-up (from Darren)

Do a CLI audit to verify there are no other inconsistencies like this.

## Comments

**CLI audit (2026-08-18, agent):** `extend` now accepts `rig extend [name] [duration]`
positionally, with `--by` kept as a synonym (duration twice = explicit error). Rest of the
surface:

- Subject shapes are otherwise consistent: `down/adopt/extend [name]` for instances,
  `logs/stop/wait <job>` for jobs with `-i` for the instance, `up [profile]`.
- One genuine outlier: volume deletion is `rig volumes --rm <name>` (a flag mutating a list
  command) where every other destructive action is verb-shaped (`rig down <name>`). Making it
  `rig volumes rm <name>` would be a breaking change — left for triage, not changed.
- Cosmetic: `--ttl`/`--by` are strings parsed with ParseDuration while `--timeout`/`--since`
  are typed cobra durations. Same accepted syntax, slightly different error text. Not worth
  churn now.
