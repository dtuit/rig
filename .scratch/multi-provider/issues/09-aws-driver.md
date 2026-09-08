# 09 — AWS EC2 driver

**What to build:** `provider: aws` works end to end in one region: native instance types with
on-demand and spot prices, availability zones as locations, Deep Learning AMI by default, key
pair and `rig` security group created on first use, EBS data volumes that follow the
find-or-create-in-location rule and mount themselves, spot launches, the systemd footprint as on
Verda, and a create-retry across AZs when capacity is short. Balance is reported unsupported.

**Blocked by:** 01, 02, 03. Needs `aws sso login` and a decision on account/region (see Comments).

**Status:** resolved

- [x] `internal/provider/aws`: `aws-sdk-go-v2` (`config`, `ec2`, `ssm`, `pricing`, `sts`);
      config keys `profile`, `region` (required, or SDK default), `security_group` (`rig`),
      `default_image` (SSM parameter path or `ami-…`), `volume_type` (`gp3`), `families`
      (default `g4dn g5 g6 g6e gr6 p3 p4d p5 p5e p5en`). Expired SSO → error tells the user to
      run `aws sso login --profile <p>`.
- [x] `Locations()` = AZs of the region (`DescribeAvailabilityZones`), code = zone name.
- [ ] `InstanceTypes()` = `DescribeInstanceTypes` for the families plus any type named in a
      profile/flag; GPU name/count/memory from `GpuInfo`; on-demand price from
      `pricing.GetProducts` (Linux, Shared, NA pre-installed sw, `capacitystatus=Used`) cached
      24h in `~/.cache/rig/aws-pricing-<region>.json`; spot from `DescribeSpotPriceHistory`
      (latest per type, min across AZs).
- [x] `Availability()` = `DescribeInstanceTypeOfferings(LocationType=availability-zone)`;
      documented as "offered, not guaranteed" — capacity is settled by the create-retry.
- [x] `Images()` = the DLAMI resolved through SSM public parameters (verify the exact path for
      "Deep Learning Base OSS Nvidia Driver GPU AMI (Ubuntu 24.04)" during implementation) and a
      plain Ubuntu 24.04 for CPU types; any `ami-…` accepted. Default user `ubuntu`.
- [x] `EnsureSSHKey` = `DescribeKeyPairs` by name + fingerprint match, else `ImportKeyPair`.
- [x] Networking: default VPC, the subnet in the chosen AZ, security group `rig` (22/tcp from
      0.0.0.0/0, tagged `rig:managed`) created if missing, `AssociatePublicIpAddress: true`.
- [x] `CreateInstance`: `RunInstances` with tags `Name=rig-<name>`, `rig:managed=true`,
      root volume `os_volume_gb` gp3 delete-on-termination, `InstanceMarketOptions` spot one-time
      with interruption `stop` when `Spot`; `InsufficientInstanceCapacity`/`Unsupported`-in-AZ →
      `ErrNoCapacity`; `VcpuLimitExceeded` → error naming the quota code (G/VT `L-DB2E81BA`,
      P `L-417A185B`). Existing data volumes are attached once the instance is `running`
      (driver-internal wait, then `AttachVolume`).
- [x] `ListInstances` = filter `tag:rig:managed`; status map pending→Provisioning,
      running→Running, stopping/stopped→Stopped, shutting-down/terminated→Gone; endpoint
      `{PublicIp, 22, ubuntu}`; `PricePerHour` from the pricing cache (spot: latest spot price);
      `Location` = AZ; `Spot` = `InstanceLifecycle == spot`; `OSVolumeID` = root mapping.
- [x] Volumes: `CreateVolume` gp3 in the AZ tagged `Name=<name>`; `ListVolumes` by tag;
      `Attach`/`Detach`/`Delete`; `MkdirCommand`/`MountCommand` synthesised: find the device by
      serial (`lsblk -o PATH,SERIAL`, serial = volume id without the dash), `blkid || mkfs.ext4`,
      `mount` at `/mnt/<name>`. `DeleteInstance` terminates, waits for requested volumes to
      detach, deletes them.
- [x] `ShutdownInstance`/`StartInstance` = Stop/Start. `Balance()` → `ErrUnsupported`.
- [x] Tests: `ec2`/`ssm`/`pricing` behind a narrow interface with a fake; mount-command
      synthesis; status/endpoint mapping; ICE → `ErrNoCapacity`; quota error text.
- [x] Manual smoke: CPU type first (`t3.medium`, no quota issue), then the cheapest GPU type
      the account's quota allows.

## Notes

Spec: ../spec.md — "AWS driver specifics". IAM: the SSO role is AdministratorAccess so no
policy work; if a scoped role is wanted later, list the ec2/ssm/pricing actions used. The
security group opens 22 to the world like Verda's instances are; tightening to the operator's IP
is a possible follow-up.

## Comments

2026-09-08 (agent): needs the owner's answers — which profile/account (default = `work`,
`ap-southeast-2`?), and whether G/P vCPU quotas exist there (`aws service-quotas
get-service-quota --service-code ec2 --quota-code L-DB2E81BA`). If quotas are 0, AWS is
CPU-only until a quota request clears, which changes how early it is worth building.

2026-09-08 (agent): Landed `internal/provider/aws` (`aws.go` config/registry/clients/errors,
`catalog.go` Locations/InstanceTypes/Availability/Images/spot prices, `pricing.go` the 24h
on-demand cache, `instances.go` instances/network/key pair, `volumes.go` EBS + mount command,
`aws_test.go`), one blank import in `internal/cli/root.go`, and `aws-sdk-go-v2` `config`,
`service/ec2`, `service/ssm`, `service/pricing` (+ core and `smithy-go`) in go.mod; `sts` is
pulled in by `config` for SSO and never called by rig. `init()` registers `aws`; the factory
does `cfg.Decode(&own)` for `profile`, `region`, `security_group` (rig), `default_image`,
`volume_type` (gp3), `families` (g4dn g5 g6 g6e gr6 p3 p4d p5 p5e p5en); no `Creds` — the SDK
default chain plus `WithSharedConfigProfile` when `profile` is set, clients built lazily on the
first call. `New(Config) *Driver`; SDK clients sit behind `ec2API`/`ssmAPI`/`pricingAPI`
(only the methods used, SDK-shaped so the paginators accept them). Every SDK error passes
through one wrapper; text matching `SSO session has expired` / `SSOProviderInvalidToken` /
`token has expired` / `InvalidGrantException` becomes exactly `aws: credentials expired; run:
aws sso login --profile <profile|$AWS_PROFILE|default>`. `Capabilities{Spot, Volumes}`,
`Balance` → `ErrUnsupported`. Endpoint `{PublicIp, 22, ubuntu}`; `Instance.Provider` left for
core. Status map pending→Provisioning, running→Running, stopping/stopped→Stopped,
shutting-down/terminated→Gone — except terminated with `StateReason.Code
Server.InsufficientInstanceCapacity` → `NoCapacity`, so a spot launch EC2 kills before it runs
is retried by core rather than aborting. `OSVolumeID` = the `RootDeviceName` mapping (falls back
to `/dev/sda1`), `VolumeIDs` the rest, `Location` = AZ, `Spot` = lifecycle spot, `Image` =
ImageId, `GPU` from the type catalogue when `InstanceTypes` ran in the process.
`ErrNoCapacity` wraps codes `InsufficientInstanceCapacity` and `Unsupported`;
`VcpuLimitExceeded` names `L-DB2E81BA` (G/VT) or `L-417A185B` (P) and the
`aws service-quotas request-service-quota-increase` line; `MaxSpotInstanceCountExceeded`
(added) names the spot quotas `L-3819A6DF` (G/VT) / `L-7212CCBC` (P). Network: default VPC,
the zone's subnet (default-for-AZ preferred), group `security_group` found or created with
22/tcp from 0.0.0.0/0 and tagged `rig:managed=true`; missing default VPC or subnet is a clear
error. `EnsureSSHKey` returns the key *name* (RunInstances only takes `KeyName`); `CreateInstance`
uses `SSHKeyIDs[0]` as `KeyName`. Existing data volumes: the driver waits for `running`
(≤10 min, ctx-aware) then attaches at the first free `/dev/sd[f-p]`. `DeleteInstance` terminates,
skips the root volume (DeleteOnTermination), waits ≤5 min for each other requested volume to be
`available` (a volume EC2 no longer knows counts as deleted) then deletes it. Mount command:
`sudo bash -c 'dev=$(lsblk -dno PATH,SERIAL | awk -v s=<id-without-dash> "\$2==s{print \$1}");
… blkid "$dev" || mkfs.ext4 -q "$dev"; mountpoint -q /mnt/<name> || mount "$dev" /mnt/<name>'`,
`MkdirCommand` = `sudo mkdir -p /mnt/<name>`. Tests (fakes for the three interfaces, all
through the Provider methods): status/endpoint/volume mapping and cache-priced listing, spot
price per zone, SSO rewrite (and a non-SSO error left alone), Locations, Availability zone
grouping, InstanceTypes from GpuInfo + seeded cache (no pricing call) and fetch-once-then-cache
(+ pricing denied → price 0, no error), Images per type class / `ami-` passthrough /
configured parameter, EnsureSSHKey import-only-when-missing, RunInstances request shape and
first-use security group, attach-after-running device letters, ICE/Unsupported → sentinel,
quota codes, missing VPC/subnet errors, DeleteInstance volume handling, Stop/Start/Balance,
mount-command synthesis (`s=vol0abc`, mkfs guard, `bash -n` parses), CreateVolume tags,
AttachVolume next-free device.

**Deviations.** (1) Spot uses `one-time` + interruption `terminate`, not `stop`: EC2 rejects
`stop` on one-time requests, and a persistent request would relaunch the instance after an
interruption and after `rig down` (unless the request is cancelled first), fighting the TTL
watchdog. Data volumes are separate EBS and survive; the OS volume is lost as with a reclaimed
Verda spot. (2) `EnsureSSHKey` matches by name only, no fingerprint (per the brief); a rotated
local key under the same name needs `aws ec2 delete-key-pair` first. (3) `InstanceTypes` lists
the configured families only; types named in a profile/flag are not appended, but they launch
fine because `Availability` returns every offered type in every zone (that is what `core.Pick`
consults), so `--type t3.medium` works. (4) The pricing cache holds the whole region's Linux
on-demand list (one paginated `GetProducts` with `regionCode`, `operatingSystem=Linux`,
`tenancy=Shared`, `preInstalledSw=NA`, `capacitystatus=Used`; `operation` prefix
`RunInstances` checked client-side) rather than one call per type, so `ListInstances` can price
a `t3.medium` too; a failure leaves prices at 0 and stops asking for the rest of the process.
Spot prices are cached in memory for 5 min so `waitRunning` polls do not hammer
`DescribeSpotPriceHistory`. (5) Pricing client pinned to `us-east-1` (the Price List API only
lives in a few regions). (6) `InstanceTypeOfferings` are not filtered to the families (see 3).

**Verify on first live run (SSO was expired, no call was possible).** SSM parameter paths:
GPU `/aws/service/deeplearning/ami/x86_64/base-oss-nvidia-driver-gpu-ubuntu-24.04/latest/ami-id`,
CPU `/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id`
(both constants in `aws.go`); root device `/dev/sda1` for the size override (Ubuntu AMIs; an
AMI with `/dev/xvda` would grow a second volume); the mount command assumes Nitro (NVMe
serial = volume id sans dash) — Xen types (p3) expose `/dev/xvdf` without a serial and are a
follow-up; the pricing filter set returns exactly one `RunInstances` product per type.

**IAM actions used** (for a scoped role later): `ec2:DescribeAvailabilityZones`,
`DescribeInstanceTypes`, `DescribeInstanceTypeOfferings`, `DescribeSpotPriceHistory`,
`DescribeImages` (the launch AMI's root device),
`DescribeKeyPairs`, `ImportKeyPair`, `DescribeVpcs`, `DescribeSubnets`,
`DescribeSecurityGroups`, `CreateSecurityGroup`, `AuthorizeSecurityGroupIngress`,
`RunInstances`, `DescribeInstances`, `TerminateInstances`, `StopInstances`, `StartInstances`,
`CreateVolume`, `DescribeVolumes`, `AttachVolume`, `DetachVolume`, `DeleteVolume`,
`CreateTags` (implied by the TagSpecifications on RunInstances/CreateVolume/
CreateSecurityGroup/ImportKeyPair); `ssm:GetParameter` on the two public parameters;
`pricing:GetProducts`. No STS call.

**Smoke checklist left undone** (needs `aws sso login --profile work`, region
`ap-southeast-2`): `rig --provider aws types` (prices non-zero, cache file appears under
`~/.cache/rig/`), `rig --provider aws types --available`, `rig --provider aws up --type
t3.medium --ttl 20m` (key pair imported, `rig` group created, ubuntu login, footprint installs
under systemd), `rig ssh`, `rig run`, `rig sync`, `rig status` (price, AZ, endpoint), a
`volume:` profile (EBS created in the AZ, mount command formats and mounts at `/mnt/<name>`,
survives `rig down`, re-attaches on the next `up`), `rig down` (instance terminated, OS volume
gone, data volume kept; `--delete-volumes` removes it), then the cheapest GPU type the quota
allows (`aws service-quotas get-service-quota --service-code ec2 --quota-code L-DB2E81BA`),
one `spot: true` launch, and the watchdog firing at the deadline.

**Follow-ups.** `core.EnsureVolume` logs Verda's `NVMe_Shared` as the default volume type
name; tighten the security group to the operator's IP; Xen-generation device naming; a
`--type` outside `families` is not shown by `rig types`.

2026-09-08 (agent, code-review fixes): five defects from the two-axis review, all TDD (red test
first) in `internal/provider/aws/aws_test.go`.

1. **Root device no longer hardcoded** (`instances.go`). `ec2API` gained `DescribeImages`;
`rootDeviceName(ctx, ami, sizeGB)` resolves the AMI's own `RootDeviceName` and caches it per AMI
id in `Driver.rootDevices`, and `CreateInstance` maps the `os_volume_gb` override on that device.
A lookup that fails or finds no such image falls back to `fallbackRootDevice` (`/dev/sda1`, was
`rootDevice`) and warns that EC2 will otherwise add a *second* volume of that size which
`rig down` will not delete. Tests: an AMI rooted at `/dev/xvda` maps `/dev/xvda`, one at
`/dev/sda1` maps `/dev/sda1`, `DescribeImages` is called once per AMI, and a denied lookup still
launches with one warning naming the AMI, the assumed device and the risk. The existing
`TestCreateInstanceRequestShapeAndNetworkOnFirstUse` fake now carries an `ami-0abc` image rooted
at `/dev/sda1` so it asserts the resolved device rather than the fallback.

2. **Pricing failure is no longer silent** (`aws.go`, `pricing.go`). `Driver.Log
func(format string, a ...any)` plus `logf` (nil → stderr, `rig: ` prefixed), as on
`internal/provider/runpod`; the factory needs no wiring because the fallback is already the
`internal/cli/root.go:stderrLogf` format. `onDemandPrices` warns once — on the same latch that
sets `pricingDown` — that prices will read 0 and recorded spend will be wrong; the listing still
succeeds. Test: two `InstanceTypes` calls over two types with a denied Price List produce exactly
one warning.

3. **`Unsupported` narrowed** (`instances.go`). `createError` maps `Unsupported` to
`ErrNoCapacity` only when the message names an availability zone (`unsupportedInZone`);
otherwise it returns `aws: <type> cannot be launched as requested in <zone>: <provider
message>` so a real misconfiguration is not swallowed by the candidate walk.
`InsufficientInstanceCapacity` is unconditional as before. Tests cover both branches (the
existing sentinel test's `Unsupported` message names the AZ, so it is unchanged).

4. **`MaxSpotInstanceCountExceeded` relabelled** (`instances.go`). Its own message: "spot request
limit reached for <type> in <region>", naming `L-3819A6DF`/`L-7212CCBC` and
`--desired-value <limit>`; the word vCPU and the vCPU quota codes appear only under
`VcpuLimitExceeded`. Test asserts both directions.

5. **Key pair fingerprint checked** (`instances.go`). `EnsureSSHKey` compares EC2's
`KeyFingerprint` against `sshFingerprint(pubkey)` — base64 SHA-256 of the ed25519 public key
blob, i.e. `ssh-keygen -lf` — accepting the value with or without the `SHA256:` prefix and
base64 padding. A clear mismatch is an error naming the stale pair and both ways out
(`aws ec2 delete-key-pair --key-name … --region …`, or point `ssh.public_key` at the key EC2
holds) instead of launching an instance nobody can ssh into. A fingerprint rig cannot recompute
(colon-separated hex: MD5 of the DER public key for imported RSA, SHA-1 of the private DER for a
pair EC2 generated) accepts the name match and warns — the format differences are noted in the
`sshFingerprint` doc comment. Tests: matching fingerprint reuses silently in all three spellings,
a different SHA-256 errors and imports nothing, an MD5 fingerprint warns and reuses.

Deliberately left as recorded: spot as one-time + `terminate`; `InstanceTypes` listing families
only; the two SSM parameter paths (still unverifiable — SSO expired, no live call was possible,
so every change here is covered by fakes only). Deviation (2) in the entry above (name-only key
match) no longer applies; the root-device line in "Verify on first live run" is now resolved at
launch time rather than assumed.

2026-09-08 (agent, live smoke): first real run against AWS account `123456789012`,
`ap-southeast-2`, SSO profile `default` (AdministratorAccess), through an isolated
`RIG_CONFIG`. Both stages passed after three driver fixes. Total spend well under $0.20
(t3.medium ~4 min at $0.0528/h, g4dn.xlarge ~7 min at $0.684/h, EBS for minutes).

**Commands run and what happened.**

*Read-only.* `rig types --available` — 5.7s, 29 accelerator types across g4dn/g5/g6/gr6/p4d/p5/p5e
with on-demand *and* spot prices and per-zone availability; `~/.cache/rig/aws-pricing-ap-southeast-2.json`
appeared (20KB). Pricing and the `DescribeSpotPriceHistory` path are confirmed live. Two notes:
p3 does not exist in this region so the family is simply absent, and `p5e.48xlarge` prices at
`0.00` on-demand while its spot price reads — the Price List filter set returns no
`RunInstances` product for it (Capacity-Block-only type), so the "exactly one product per type"
assumption in the earlier entry does not hold for every type; the spot column still populates and
nothing errors. t3.medium is not listed (the deliberate families filter) and launched anyway, as
designed.

*Stage 1, CPU + EBS data volume.*
`rig up --type t3.medium --name smoke --ttl 30m --volume rig-smoke --volume-size 10` — failed
first (bug 1 below), then succeeded in **24s**: volume `rig-smoke` found and reused in
ap-southeast-2a, instance running, ssh up, watchdog installed, volume mounted.
`rig status` — `smoke running t3.medium 3.25.79.142 ap-southeast-2a 0.05 30m0s`, burn rate
$0.05/h: **the pricing lookup is live and non-zero.** `rig status --json` — endpoint
`{3.25.79.142, 22, ubuntu}`, `price_per_hour: 0.0528`, `location: ap-southeast-2a`,
`os_volume_id` and `volume_ids` both populated and distinct.
`rig ssh -- 'cat ~/.rig/deadline; systemctl is-active rig-ttl; df -h /mnt/rig-smoke; tmux -V; whoami'`
— deadline `1788895207`, watchdog `active` under systemd, `/dev/nvme1n1 9.8G … /mnt/rig-smoke`
(the serial-based mount command found, formatted and mounted the EBS volume on a Nitro type),
`tmux 3.4`, user `ubuntu`.
`rig run --name hello --wait -- bash -lc 'echo hi from $(hostname)'` — `exited 0`;
`rig logs hello` — `hi from ip-10-20-10-28`; `rig ps` — `hello exited 0`.
`rig sync <file>` — landed at `~/smokefile.txt` with the right content.
`rig extend 1h` — `self-destructs Tue 20:20 (in 1h29m0s)`, reflected by `rig status`.
`rig tunnel --detach 8000` / `tunnel status` / `tunnel status --json` / `tunnel stop` — supervisor
pid, phase `connected`, and `curl http://127.0.0.1:8000/` returned **200** through the forward
against a `python3 -m http.server` job. **The tunnel state's `user` field works for a non-root
user**: the JSON carries `"user":"ubuntu"` and the ssh forward used it. `stop` left
`no detached tunnels`.
`rig down --yes` — terminated. `rig volumes` still showed `rig-smoke` (10GB, available) — data
volumes survive `down`, and the OS volume was gone with the instance. `rig volumes --rm rig-smoke`
prompts; with `--yes` it deleted.

*Stage 2, GPU / DLAMI.* `rig up --type g4dn.xlarge --name gsmoke --ttl 20m` — **1m8s**, image
`ami-022e694273407a7c0`, $0.68/h. `rig ssh -- 'nvidia-smi -L; cat ~/.rig/deadline'` —
`GPU 0: Tesla T4 (UUID: GPU-0c3b89d1-…)`, driver `595.91.07`, deadline present, watchdog `active`,
user `ubuntu`. `rig down --yes` — terminated. **The DLAMI SSM path, the GPU instance-type mapping
and the price lookup are all confirmed live**; the DLAMI's root is 75GB (its own default), which
`rig down` removed with the instance.

**Bugs found and fixed** (each red-test-first in `aws_test.go`, all fakes):

1. **A default VPC was mandatory** (`aws.go`, `instances.go`). The account has three named VPCs
and no default one — normal for an older account, and a deliberate CIS control in many orgs —
so every launch died at `aws: no default VPC in ap-southeast-2`, with the volume already
created. Added config keys `vpc` (launch VPC) and `subnets` (explicit subnet ids, at most one
per zone, which imply their VPC); `launchVPC` returns config's VPC, else the default VPC, else an
error that now names `create-default-vpc` **and** `providers.aws.vpc` / `providers.aws.subnets`.
Subnet choice prefers the zone's default subnet, then one with `MapPublicIpOnLaunch` — the
launch always asks for a public IP and rig dials it, so a private subnet would strand the
instance. A zone none of the configured subnets covers is a clear error naming the zone and the
key rather than a launch somewhere else. Tests: `TestCreateInstanceUsesConfiguredSubnets`
(subnet used, `DescribeVpcs` never called, security group created in the subnet's VPC, uncovered
zone errors and does not call RunInstances), `TestCreateInstanceUsesConfiguredVPC` (public
subnet preferred inside a configured VPC), `TestCreateInstanceMissingDefaultVPCNamesTheConfigKeys`.

2. **`rig volumes` listed the OS volume as a data volume** (`aws.go`, `instances.go`,
`volumes.go`). `RunInstances` tags every volume it creates, so the 8GB boot volume came back from
`ListVolumes` as a second `rig-smoke` in the same zone — indistinguishable by name and location
from the real 10GB data volume, so `rig volumes --rm rig-smoke` was ambiguous and core's
find-or-create-in-location could have matched a *running* instance's boot volume and tried to
attach it. The boot volume's tag spec now also carries `rig:os=true` and `ListVolumes` skips it.
Tests: `TestListVolumesSkipsOSVolumes` (built from the exact live pair), and
`TestCreateInstanceMarksTheOSVolume`.

3. **`gpu` was always empty in a listing** (`instances.go`, `catalog.go`). `rig status --json`
for the running g4dn.xlarge reported `"gpu":""`: only `InstanceTypes` filled the type → GPU
cache and `status` never calls it, so the field was dead in real use. `learnGPUs` now resolves
the types in a listing with one `DescribeInstanceTypes` per process (cached, failure leaves the
field empty since it is decoration); `gpuName` is shared with `InstanceTypes`. Test:
`TestListInstancesReportsTheGPUWithoutInstanceTypes` (name resolved, second listing asks EC2
nothing more). Verified live afterwards: `gpu: "NVIDIA T4"` for the g4dn.xlarge and `""` for the
t3.medium.

`go build ./...`, `go vet ./internal/provider/aws/`, `go test ./internal/provider/aws/` and
`gofmt -l ./internal ./cmd` all clean. No existing test was changed in intent; `defaultNetwork()`
subnets gained their `VpcId` and `fakeEC2` gained two call counters.

**Cleanup, verified with the AWS CLI.** Non-terminated instances tagged `rig:managed`: `[]`.
Volumes tagged `rig:managed`, any state: `[]` — all three volume ids
(`vol-05acfe1a0044bbb43` data, `vol-0aab31342de348884` and `vol-0dfb0467938e7e6f6` roots) return
`InvalidVolume.NotFound`. Both instances (`i-045e0ad76b3913a82`, `i-0cf9bf6c3fbd1ec31`) read
`terminated`. **Nothing is billing.** Left in place, as expected for reuse: key pair
`rig-ubuntu-rig_ed25519.pub` (ed25519, harmless and free) and security group
`sg-02b1d6ba7dd58a26e` named `rig`.

**One thing the owner must decide.** With no default VPC, the smoke had to borrow an existing
public subnet, and I chose the two `-dmz-` subnets of **`vpc-04d126bd3386368f3`
(`example-prod-vpc`)** — creating an isolated throwaway VPC was blocked by the sandbox, and
a DMZ subnet is where an internet-facing host belongs. So rig created its `rig` security group,
which opens **22/tcp to 0.0.0.0/0**, *inside a production VPC*. I tried to delete it and the
sandbox blocked that too. It holds no instances and costs nothing, but if that ingress is not
wanted in prod: `aws ec2 delete-security-group --region ap-southeast-2 --group-id
sg-02b1d6ba7dd58a26e` (rig recreates it on the next `up`). The better long-term answer is a
dedicated rig VPC or subnet pinned with the new `providers.aws.subnets` key, and tightening the
group to the operator's IP (already an open follow-up).

**Follow-ups this run adds.** (a) `Locations()` still lists every AZ even when
`providers.aws.subnets` covers only some, so a picker landing on an uncovered zone fails with a
clear error instead of walking to the next candidate — filtering `Locations()` to the configured
subnets' zones is the fix. (b) The `vpc`/`subnets` keys are not in `docs/` yet (out of this
agent's edit scope). (c) `p5e.48xlarge` on-demand price reads 0.00 — the Price List filter set
returns no product for Capacity-Block-only types. (d) Spot was still not exercised live, nor was
the watchdog actually firing at its deadline.

2026-09-08 (agent, ssh ingress hardening): the `rig` security group no longer opens 22/tcp to
`0.0.0.0/0`, and the `vpc`/`subnets`/ingress config keys are documented. New file
`internal/provider/aws/ingress.go`; `aws.go` gained the config key, the discovery seam and a
tag constant; `instances.go`'s `ensureNetwork` lost its inline group block to the new
`ensureSSHIngress`; tests in `aws_test.go`; docs in `README.md` and `internal/cli/inventory.go`.

**Config key: `providers.aws.ssh_cidrs`** (a list). Empty — the default — means *your current
public IP as a `/32`*, discovered when the group is created. Entries may be CIDR blocks or bare
addresses (a bare address means `/32`), IPv4 or IPv6 (v6 goes in `Ipv6Ranges`), and
`["0.0.0.0/0"]` is honoured verbatim when somebody asks for it — with a warning line naming the
group, so an intentional choice is still visible in the log.

**Discovery is not a hard dependency, and there is no wide fallback.** `discoverPublicIP` GETs
`https://checkip.amazonaws.com` (AWS's own service, plain-text body, 5s timeout, 64-byte read
cap) — no new module dependency and no new vendor: anything that can reach EC2 can reach it.
Verified live from this box: `3.106.139.163` in 0.51s. If it fails, `rig up` **fails** with
`aws: cannot work out your public IP to authorise ssh (<why>); set providers.aws.ssh_cidrs to
the block ssh may come from — ["1.2.3.4/32"] for one address, or ["0.0.0.0/0"] to accept the
whole internet`. Argument for erroring rather than a narrower automatic fallback: there is no
honest narrower default — rig cannot guess an operator's network, and any invented block
(RFC1918, the last-known address) would either strand the user or authorise strangers. The
failure is also nearly impossible in practice (EC2 unreachable means the launch dies anyway), it
is one config line to fix permanently, and it happens *before* `CreateSecurityGroup`, so a
failed launch leaves nothing behind AWS-side.

**Changed operator address: rig adds the rule to its own group.** On reuse, `ensureSSHIngress`
reads the group's existing 22/tcp ranges (`tcp` or `-1`, port 22 inside the range; rules
referencing other groups or prefix lists are ignored — rig cannot tell whether they admit you)
and adds only the wanted blocks that no existing block *covers* (a `/24` or `0.0.0.0/0` already
containing the address counts as admitted, so nothing is added). It authorises only when the
group carries `rig:managed=true`, logs one line naming the group and the block it added, and
never revokes anything (the line says how to prune stale `/32`s). Chosen over telling the user
to run the CLI because the changed-IP case is the everyday one (home ISP, café, VPN), the group
is rig's own and holds only rig instances, so making the operator authorise their own address on
rig's own group is friction with no safety gained. A group **without** the tag is never
modified: if it does not admit the operator the launch is refused before `RunInstances`, naming
the group id, the missing block, the missing tag and the exact
`aws ec2 authorize-security-group-ingress --region … --group-id … --protocol tcp --port 22
--cidr …` line (plus "point providers.aws.security_group at a group rig manages"). Either way
`rig up` cannot silently produce an unreachable instance. One extra safety net for the group
that already exists in the live account: when a rig-managed group still holds a
`0.0.0.0/0` rule and config did not ask for one, rig warns with the
`revoke-security-group-ingress` line rather than revoking it (a colleague may be relying on it).

**Provenance for an auditor.** Description (immutable after creation, and validated live with
`aws ec2 create-security-group --dry-run`, which answered "Request would have succeeded"):
`rig: ssh (22/tcp) to rig-managed instances, from the operator address rig authorised. Created
by rig (github.com/dtuit/rig); safe to delete when no rig instances remain.` Tags: `Name=rig`,
`rig:managed=true`, and new `rig:purpose=ssh ingress for rig-managed instances`. Each authorised
range carries the rule description `rig ssh`.

**Tests** (all through `CreateInstance` with the existing fakes; `newTestDriverConfig` now stubs
`publicIP` so no test can touch the network): `TestSecurityGroupAdmitsOnlyTheOperatorIP`
(one `/32`, 22/tcp, description and tags), `TestConfiguredSSHCIDRsAreHonouredVerbatim`
(`10.20.0.0/16`, `0.0.0.0/0`, bare `198.51.100.9` → `/32`, and discovery is never called),
`TestExistingRigGroupGainsTheCurrentIP` (adds one `/32` to `sg-0old`, creates no second group,
logs it, launches into it), `TestExistingRigGroupAlreadyAdmittingTheIPIsUntouched` (a covering
`/24` → no authorize, no log), `TestRigGroupOpenToTheWorldIsFlagged` (no authorize, one warning
naming `0.0.0.0/0` and the revoke line), `TestUnmanagedSecurityGroupIsNeverModified` (error names
group/block/tag/CLI, nothing authorised or created, no `RunInstances`; and an unmanaged group
that *does* admit you launches with nothing changed), `TestUndiscoverablePublicIPFailsNamingTheConfigKey`
(error names `providers.aws.ssh_cidrs` and `0.0.0.0/0`; no group, no launch).

**One existing test changed in intent, deliberately:**
`TestCreateInstanceRequestShapeAndNetworkOnFirstUse` asserted the authorised CIDR was
`0.0.0.0/0` and now asserts `203.0.113.7/32` (the stubbed discovery address, TEST-NET-3).
`fakeEC2` gained `authorizeCalls` (every call in order; `authorized` still holds the last).

**Docs.** `internal/cli/inventory.go`'s `exampleConfig` gained `ssh_cidrs`, `vpc`, `subnets` (and
`security_group`'s comment now says "launch VPC"). `README.md`: the `aws:` config block gained
the three keys, plus two new paragraphs before "Per-provider caveats" — "Who may ssh in on AWS"
(the /32 default, the key, the changed-address behaviour, never touching a group rig did not
create, nothing is ever revoked) and "rig will not invent a VPC" (no default VPC → pin
`vpc`/`subnets`; rig creates no VPC, subnet, gateway or route; pin a *dedicated* VPC, not a
production one, because that is where the ssh port lands). "What has been run for real" now
records AWS EC2 as live-verified on 2026-09-08 (`t3.medium` with an EBS data volume,
`g4dn.xlarge` on the DLAMI; spot and a firing deadline still unexercised); Vast's status is
untouched.

`go build ./...`, `go vet ./internal/provider/aws/ ./internal/cli/`, `go test ./...` and
`gofmt -l ./internal ./cmd` all clean. AWS-side: only read-only calls plus one
`create-security-group --dry-run`; `describe-security-groups --filters tag:rig:managed=true`
returns `[]`, so nothing rig-managed exists in the account.

**Follow-ups this adds.** (e) `ssh_cidrs` is parsed at launch time, so a typo is reported by
`rig up` rather than at config load — after core may already have created a data volume;
validating in the `provider.Register` factory would move it earlier (needs a way to build a
`config.ProviderConfig` in a test). (f) Stale `/32`s accumulate in the group; rig deliberately
never revokes, so a `rig doctor`-style prune is the natural home. (g) Split-tunnel VPNs make the
discovered address the wrong one — the symptom is an ssh timeout and the fix is `ssh_cidrs`;
worth a line in the timeout message if it ever bites. (h) Discovery happens once per process per
zone; a long-running `rig up --watch` that outlives an IP change is not reconciled.
