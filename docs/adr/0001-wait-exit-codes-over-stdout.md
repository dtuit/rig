# `rig wait` reports job state over stdout, not ssh exit status

`rig wait <job>` must exit with the job's own exit code, so agents can write
`rig run --wait -- cmd && next`. The remote poll loop therefore prints a result token
(`EXIT <code>`, `TIMEOUT`, `NOJOB`) on stdout and rig parses that, rather than propagating the
code through ssh's exit status. ssh's exit status stays reserved for transport failure (255),
so a dropped connection is distinguishable from a job that exited 255, and rig can reconnect
and resume the wait instead of misreporting. Exit code 124 is reserved for `--timeout` expiry
(same convention as GNU timeout); a job that itself exits 124 is indistinguishable, which we
accept and document.

Polling runs server-side in bounded chunks over the multiplexed ssh connection — not one
client-side ssh handshake per tick, and not one unbounded remote loop that a network blip
would silently kill.
