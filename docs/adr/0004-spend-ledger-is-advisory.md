# The spend ledger is advisory, not authoritative

`rig spend` reads a local append-only ledger (`~/.config/rig/ledger.jsonl`) written on up and
down, breaking the otherwise strict "no local state" rule because the provider's billing export
answers "what did last week cost" only after painful reverse-engineering. The ledger is
deliberately advisory: instances torn down by the TTL watchdog (or from another machine) get no
down event, so their cost is estimated through the recorded TTL and marked as such, and the
provider's billing remains the source of truth. Events carry the provider they happened on
(absent means Verda, so a ledger written before rig had other drivers reads unchanged), so two
clouds' identically named instances never merge and
`rig spend` subtotals per provider once more than one appears. We chose this over querying the
provider's billing API (not exposed by the SDK) and over treating the ledger as authoritative
(it can't see teardowns rig didn't perform).
