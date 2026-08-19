# Handoff: make rig agent-native

**Status:** ready-for-agent
**Written:** 2026-08-18
**Feature slug:** `agent-ergonomics`

## Why this exists

rig's README says it "provisions ephemeral GPU instances … and gets you (or your AI agent) onto
them". The agent half of that claim is the weak half. This handoff comes out of driving rig
end-to-end, unattended, through a multi-day ML experiment: three instances (`CPU.4V.16G`, one H100,
one A100), ~40 GPU-hours, a 27-run training matrix, and one ~8-hour unattended sweep.

**The core loop is right and should not change.** `up → sync → run → logs → down` was never the
thing I wanted to be different. Three design decisions in particular earned their keep and should be
protected in any refactor:

- **TTL-by-default.** It targets the real failure mode — a forgotten box at $43/day — not an
  imagined one. It is what made it safe to walk away from an 8-hour matrix.
- **`rig types --available`** showing live availability, on-demand *and* spot price, and location in
  one table. When the A100 I wanted was unavailable I substituted an H100 and understood the cost
  delta in a single command, without opening a provider console.
- **Detached jobs with recorded exit codes** (`~/.rig/logs/<job>.log` + `<job>.exit`). Nothing was
  ever lost to a disconnect. `.exit` is a good primitive and everything below builds on it.

Also right, and worth stating so nobody "simplifies" them away: the split between a passphrase-less
instance key and a separately-forwarded read-only Bitbucket key (private `uv sync` git dependencies
worked first time on all three boxes), and not creating a data volume unless asked.

**What is wrong is the interface, not the model.** Almost every hour I lost went to rig being shaped
for a human at a terminal — blocking on `rig logs -f`, reading a table, answering a prompt. An agent
cannot watch a stream or read a table. That is the whole of the work below.

## Repo conventions to follow

Read `AGENTS.md` first. Relevant here:

- Issues are local markdown under `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from
  `01`, one file per ticket, with a `Status:` line. See `docs/agents/issue-tracker.md`.
- Triage labels are the five in `docs/agents/triage-labels.md`.
- `AGENTS.md` promises a root `CONTEXT.md` and `docs/adr/`. **Neither exists yet.** Items 1 and 2
  below are the first changes big enough to deserve an ADR, so this is a good moment to create both.

**First action for whoever picks this up:** split the items below into
`.scratch/agent-ergonomics/issues/01..07-*.md` per the convention, then work them in order. This
document is the source; do not re-derive it.

## The work, in priority order

### 01 — `rig wait` (highest value by a distance)

*Problem.* There is no way to block until a job finishes. Driving an 8-hour matrix cost roughly
forty polling round-trips of the form
`until rig ssh -- 'test -f ~/.rig/logs/x.exit'; do sleep 180; done`. Every one is a shell out, an
ssh handshake, and an agent turn.

*Shape.* `rig wait <job> [--timeout D] [-i inst]` — blocks until the job leaves the running state,
then exits with the job's own exit code. Plus `rig run --wait` for the common case of "start it and
tell me when it's done".

*Where.* `internal/cli/jobs.go` (commands) and `internal/remote/remote.go:135` — the `Job` struct
already carries `ExitCode`, and `remote.Jobs()` already parses it at `remote.go:188`. The state this
needs is present; only the blocking loop and exit-code propagation are missing. Poll server-side
over one ssh session rather than reconnecting per tick.

*Done when.* A job that exits 0 makes `rig wait` exit 0; a job that exits non-zero propagates that
code; `--timeout` exits distinguishably (use a reserved code, e.g. 124, and document it);
`rig run --wait` is equivalent to `rig run` followed by `rig wait`.

### 02 — machine-readable output (`--json`)

*Problem.* Every status read had to be scraped:
`rig ps | awk '/^train/{print $2}' | tr -d ' \r'`. That is brittle against any formatting change
and it is the only option available.

*Shape.* A persistent `--json` on the root command (`internal/cli/root.go:49`), honoured by
`status`, `ps`, `types` and `volumes`. Emit the existing structs; do not invent a parallel schema.

*Where.* `internal/cli/lifecycle.go:217` (status render, burn-rate calc at `:233–251`),
`internal/cli/jobs.go:71` (ps), `internal/cli/inventory.go:20` (types), `:104` (volumes).

*Done when.* `rig status --json | jq -r '.[0].ttl'` works, and the human tables are byte-identical
to today when `--json` is absent.

### 03 — `rig sync --exclude`

*Problem.* I needed a dataset without its 1.2 GB `images/` subdirectory and had to build a pruned
hardlink tree by hand to get it.

*Shape.* Repeatable `--exclude PATTERN`, passed straight through to rsync.

*Where.* `internal/cli/access.go:90` (flag) → `internal/sshx/sshx.go:154` (where the rsync argv is
built). Commit `c126e34` already excludes `.env` by default, so the mechanism exists — this
generalises it. Keep the default exclusions on top of any user patterns.

### 04 — fetch data on the instance, not through the operator's machine

*Problem.* The single biggest wall-clock waste in the whole exercise. A 1.2 GB dataset that
originated in S3 was pushed *through my box* to the instance at ~50 MB/min — about 20 minutes. The
instance has far better bandwidth to S3 than the operator does.

*Shape.* `rig fetch <url> [--dest]` that runs the transfer on the instance (curl for http(s), the
provider/AWS CLI for s3://).

*Open question for the repo owner, do not decide this unilaterally:* this needs credentials on an
ephemeral box. Options are forwarding short-lived creds, expecting presigned URLs, or supporting only
public/http sources. There is a deliberate design stance in rig today of keeping only a read-only
Bitbucket key on instances; putting long-lived AWS keys there would break that stance. Raise it
before implementing. If the answer is "presigned only", the command is trivial and still worth having.

### 05 — cost accounting that survives teardown

*Problem.* `rig status` shows burn rate while an instance lives, and then the number disappears at
`rig down`. Reporting spend meant reconstructing `$1.79/h × 6.1h` by hand from memory. Separately,
enough demand exists for this that a standalone audit exercise has been done reverse-engineering
spend from provider audit-log exports — that work would be unnecessary if rig recorded it at source.

*Shape.* Two parts. (a) `rig down` prints the instance's lifetime and cost as it deletes.
(b) A local append-only ledger (e.g. `~/.config/rig/ledger.jsonl`, a record on up and on down) plus
`rig spend [--since D]` to summarise it.

*Where.* `internal/cli/lifecycle.go:112` (down) and the same price fields the status renderer already
uses at `:241–251`. Treat the ledger as advisory, not authoritative — the provider's billing is the
source of truth and the ledger will miss instances torn down by TTL rather than by `rig down`, which
is worth handling explicitly.

### 06 — `rig extend` argument shape is inconsistent

*Problem.* `down`, `logs` and `stop` all take their subject positionally, so `rig extend grid422 8h`
reads natural — and fails with `accepts at most 1 arg(s), received 2`. The real form is
`rig extend grid422 --by 8h`.

*Shape.* Accept an optional positional duration as a synonym for `--by`, or fail with a message that
names `--by` instead of the generic cobra arity error. Low effort, removes a guaranteed stumble.

*Where.* `internal/cli/lifecycle.go:175`.

*Darren: We should do a CLI audit to verify that there are no other inconsistencies like this.*

### 07 — decide and document the exit-status contract for `rig ssh -- <cmd>`

*Problem.* `rig ssh -- 'pkill -f something'` exits 255, because the remote command tears down its own
process tree and ssh reports that. This is arguably correct, but it is undocumented and it makes
`rig ssh -- ... && next` unusable without knowing to expect it.

*Note:* I initially recorded this as a `rig stop` bug. It is not — `stop` returns cleanly
(`internal/cli/jobs.go:132–150`) and the 255 came from a separate `rig ssh` in the same shell line.
Do not go looking for a bug in `stop`.

*Shape.* Decide whether remote exit codes propagate (probably yes), then document it in the `ssh`
command's help text and README.

## Out of scope

The TUI, the provider abstraction, and the `up`/`down` lifecycle itself. Nothing below the CLI
surface needs to change for any of the above.

## Mistakes I made that the docs could have prevented

Worth reading as discoverability signal rather than as a to-do list. In each case I guessed and rig
did not stop me:

- I used `yes | rig down <name>` for an entire session. **`-y/--yes` already exists**
  (`lifecycle.go:166`). I never ran `--help`.
- I spent several turns concluding `rig logs` was lagging behind the on-box log file and switched to
  `rig ssh -- tail ...`. **`rig logs` defaults to `-n 100`.** The tool was fine; I misdiagnosed it.
- I got `rig extend`'s argument shape wrong (item 06).

If the agent that is meant to be a first-class user guesses wrong three times in one session, the
affordances are too quiet. Consider whether `--help` output, or an error message that names the right
flag, is the cheaper fix than any of the features above.

## Suggested skills

- `mattpocock-skills:domain-modeling` — first, to create the `CONTEXT.md` and `docs/adr/` that
  `AGENTS.md` already promises. Items 01 and 02 change the CLI contract and deserve an ADR each.
- `mattpocock-skills:codebase-design` — for item 02 specifically: `--json` is a seam question (where
  does rendering end and data begin) rather than a formatting question.
- `mattpocock-skills:tdd` — item 01 has a crisp contract (blocks, propagates exit code, times out)
  and is well suited to being written test-first.
- `mattpocock-skills:code-review` — before merging, review against the repo's own standards.
- `/wayfinder` — if items are worked in parallel; `docs/agents/issue-tracker.md` documents the map
  and claim/resolve protocol.

## First steps

1. Read `AGENTS.md` and `docs/agents/issue-tracker.md`.
2. Split this document into `.scratch/agent-ergonomics/issues/01..07-*.md` per the convention.
3. Raise item 04's credential question with the repo owner before implementing it.
4. Start with 01 and 02 — they are the difference between a good human tool that agents drive
   awkwardly and one that is genuinely agent-native.
