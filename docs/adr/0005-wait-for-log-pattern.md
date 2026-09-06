# `rig wait --for` matches the job log server-side and exits 125 when the job ends first

`rig wait <job> --for '<regex>'` blocks until a line of the job's log matches an extended
regex (grep -E), prints that line on stdout, and exits 0, so an agent can catch a stage
transition or read one progress line mid-run without hand-rolling an ssh tail-and-grep loop.
It reuses the wait machinery from ADR-0001 rather than streaming `rig logs -f` through a
client-side matcher: the remote poll script greps the log in the same bounded chunks it polls
the exit file in, and reports a fourth stdout token, `MATCH <line>`, beside `EXIT`, `NOJOB`,
and `TIMEOUT`. The match is re-checked right before `EXIT` is reported so a line written in
the job's last moments is not lost to the poll interval.

Exit codes: 0 with the matched line on stdout; 124 on `--timeout` (as for a plain wait);
125 when the job ends before any line matches. 125 was chosen because it sits beside 124 in
the same "the wrapper, not the job, is reporting" range that GNU timeout uses, and no rig
command otherwise exits 125. The job's own exit code is not propagated in this mode — the
caller asked about a log line, not about completion — and is named in the stderr message
instead. The regex reaches the instance single-quoted (`internal/shellq`), so grep sees it
exactly as typed. Log content only ever reaches the poll's stdout inside the `MATCH` token,
so a log line that itself begins with `EXIT `, `NOJOB`, or `TIMEOUT` cannot be mistaken for
a state token.
