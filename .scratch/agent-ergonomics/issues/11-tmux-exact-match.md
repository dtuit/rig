# 11 — rig targets tmux sessions by prefix, so sibling job names collide

Status: resolved
Source: agent session d301490d (training run on one H100, 2026-09-06); code read against ec9471b

## Problem

Jobs run in tmux session `rig-<name>`. Rig checks liveness with `tmux has-session -t rig-<name>`
(`internal/remote/wait.go:109`) and kills with `tmux kill-session -t rig-<name>`
(`internal/remote/remote.go:210`). tmux resolves `-t` by exact name first, then by unique prefix.
Verified on tmux 3.4: with only `rig-pipeline2` alive,

```
tmux has-session -t rig-pipeline   # exit 0 (matched rig-pipeline2)
tmux kill-session -t rig-pipeline  # kills rig-pipeline2
```

Consequences:

- `rig stop pipeline` kills `pipeline2` once `pipeline` itself has exited.
- `rig wait pipeline` for a name that never existed never reports NOJOB while a prefix sibling
  runs (the exit-file check comes first, so a job that did run is still reported correctly).

The session used exactly the names `pipeline`, `pipeline2`, `pipeline3`; the agent's own
queue script hit the same fault and idled the GPU for about three minutes.

## Shape

Use tmux's exact-match form: `-t =rig-<name>` in both places. `ListJobs` and the TTL watchdog
already use `tmux ls -F` with anchored greps and are fine. `new-session -d -s` is a create, not a
lookup, so the "duplicate session" check is unaffected.

## Where

`internal/remote/wait.go:109`, `internal/remote/remote.go:210`. Add a `wait_test.go` case that
asserts the generated script contains `=rig-`.

## Comments

**Fixed (2026-09-06, agent):** both lookups use tmux's exact-match target: `waitScript` in
`internal/remote/wait.go` emits `tmux has-session -t =rig-<name>` and `StopJob` in
`internal/remote/remote.go` runs `tmux kill-session -t =rig-<name>`. `new-session -d -s`,
`ListJobs`, and the watchdog's anchored `grep '^rig-'` were already exact and are untouched.
`TestWaitScriptTargetsExactSession` in `internal/remote/wait_test.go` asserts the `=rig-`
form.
