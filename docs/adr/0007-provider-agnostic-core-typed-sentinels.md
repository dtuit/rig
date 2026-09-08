# The core is provider-agnostic through typed sentinels, and capacity is settled at create time

rig drives four clouds behind one `provider.Provider`, so nothing above the interface may know
which cloud it is on. Two rules make that hold. Statuses are normalised to constants
(`Provisioning`, `Running`, `Stopped`, `Error`, `Gone`, `NoCapacity`), with the provider's own
word kept in `RawStatus` for display, so no comparison against a Verda string survives in core,
cli, tui or the instance cache. Feature gaps are typed rather than guessed: `ErrUnsupported`
means "this cloud cannot do that" — spot on RunPod's v2 API, data volumes on Vast.ai, an account
balance on AWS — and `Capabilities{Spot, Volumes, Balance, DockerImages, SelfStop}` lets `up`
refuse a `spot: true` or `volume:` profile before it spends a create, while the dashboard hides
what a provider cannot report. A gap is always an error the user can read, never a silent
degradation.

Capacity is settled at create, not pre-checked. `core.Pick` returns an ordered candidate list of
(type, location) pairs — profile preference order, locations already holding the profile's volume
first — and `Up` walks it, calling `CreateInstance` per candidate and moving to the next only on
`ErrNoCapacity`; every other error aborts the launch as before. This is the only shape that works
on all four clouds: AWS reports `InsufficientInstanceCapacity` at `RunInstances` and nowhere
earlier, and marketplace stock evaporates between the availability call and the create — the
RunPod smoke run (2026-09-08) did exactly that, failing on a GPU the catalogue had listed as
available seconds before. An availability listing is therefore advisory shopping data, like the
ledger and the instance cache (ADR-0004, ADR-0006); the create is the only truth.

Decided with the repo owner (2026-09-08) over a pre-flight capacity check (a lie on AWS and a
race everywhere else), a stringly-typed `Supports("spot")` query (no help to `up` before it
spends money, and untypeable), and per-driver retry loops (four clouds each reinventing
placement, without the preference list only rig knows).
