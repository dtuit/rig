# `--json` renders the existing structs, not a parallel schema

A persistent root `--json` flag makes `status`, `ps`, `types`, `volumes`, and `spend` emit JSON for
agents to consume with jq. The JSON is the existing internal structs (`provider.Instance`,
`provider.InstanceType`, `provider.Volume`, `remote.Job`) given lowercase `json` tags — plus
thin per-command wrappers only where the human table already shows computed values (status
adds `ttl`, types adds `available`). We deliberately did not design a versioned output schema:
the structs are the schema, and a field rename is a breaking change to both Go callers and
scripts alike, which keeps the contract honest.

Commands are split at a gather/render seam: build the rows first, then hand them to exactly
one of the two renderers. The human tables must stay byte-identical to the pre-`--json`
output; JSON list commands emit `[]` rather than prose like "no jobs".
