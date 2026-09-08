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
- [ ] `Images()` = the DLAMI resolved through SSM public parameters (verify the exact path for
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
- [ ] Manual smoke: CPU type first (`t3.medium`, no quota issue), then the cheapest GPU type
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
