# 09 — AWS EC2 driver

**What to build:** `provider: aws` works end to end in one region: native instance types with
on-demand and spot prices, availability zones as locations, Deep Learning AMI by default, key
pair and `rig` security group created on first use, EBS data volumes that follow the
find-or-create-in-location rule and mount themselves, spot launches, the systemd footprint as on
Verda, and a create-retry across AZs when capacity is short. Balance is reported unsupported.

**Blocked by:** 01, 02, 03. Needs `aws sso login` and a decision on account/region (see Comments).

**Status:** needs-info

- [ ] `internal/provider/aws`: `aws-sdk-go-v2` (`config`, `ec2`, `ssm`, `pricing`, `sts`);
      config keys `profile`, `region` (required, or SDK default), `security_group` (`rig`),
      `default_image` (SSM parameter path or `ami-…`), `volume_type` (`gp3`), `families`
      (default `g4dn g5 g6 g6e gr6 p3 p4d p5 p5e p5en`). Expired SSO → error tells the user to
      run `aws sso login --profile <p>`.
- [ ] `Locations()` = AZs of the region (`DescribeAvailabilityZones`), code = zone name.
- [ ] `InstanceTypes()` = `DescribeInstanceTypes` for the families plus any type named in a
      profile/flag; GPU name/count/memory from `GpuInfo`; on-demand price from
      `pricing.GetProducts` (Linux, Shared, NA pre-installed sw, `capacitystatus=Used`) cached
      24h in `~/.cache/rig/aws-pricing-<region>.json`; spot from `DescribeSpotPriceHistory`
      (latest per type, min across AZs).
- [ ] `Availability()` = `DescribeInstanceTypeOfferings(LocationType=availability-zone)`;
      documented as "offered, not guaranteed" — capacity is settled by the create-retry.
- [ ] `Images()` = the DLAMI resolved through SSM public parameters (verify the exact path for
      "Deep Learning Base OSS Nvidia Driver GPU AMI (Ubuntu 24.04)" during implementation) and a
      plain Ubuntu 24.04 for CPU types; any `ami-…` accepted. Default user `ubuntu`.
- [ ] `EnsureSSHKey` = `DescribeKeyPairs` by name + fingerprint match, else `ImportKeyPair`.
- [ ] Networking: default VPC, the subnet in the chosen AZ, security group `rig` (22/tcp from
      0.0.0.0/0, tagged `rig:managed`) created if missing, `AssociatePublicIpAddress: true`.
- [ ] `CreateInstance`: `RunInstances` with tags `Name=rig-<name>`, `rig:managed=true`,
      root volume `os_volume_gb` gp3 delete-on-termination, `InstanceMarketOptions` spot one-time
      with interruption `stop` when `Spot`; `InsufficientInstanceCapacity`/`Unsupported`-in-AZ →
      `ErrNoCapacity`; `VcpuLimitExceeded` → error naming the quota code (G/VT `L-DB2E81BA`,
      P `L-417A185B`). Existing data volumes are attached once the instance is `running`
      (driver-internal wait, then `AttachVolume`).
- [ ] `ListInstances` = filter `tag:rig:managed`; status map pending→Provisioning,
      running→Running, stopping/stopped→Stopped, shutting-down/terminated→Gone; endpoint
      `{PublicIp, 22, ubuntu}`; `PricePerHour` from the pricing cache (spot: latest spot price);
      `Location` = AZ; `Spot` = `InstanceLifecycle == spot`; `OSVolumeID` = root mapping.
- [ ] Volumes: `CreateVolume` gp3 in the AZ tagged `Name=<name>`; `ListVolumes` by tag;
      `Attach`/`Detach`/`Delete`; `MkdirCommand`/`MountCommand` synthesised: find the device by
      serial (`lsblk -o PATH,SERIAL`, serial = volume id without the dash), `blkid || mkfs.ext4`,
      `mount` at `/mnt/<name>`. `DeleteInstance` terminates, waits for requested volumes to
      detach, deletes them.
- [ ] `ShutdownInstance`/`StartInstance` = Stop/Start. `Balance()` → `ErrUnsupported`.
- [ ] Tests: `ec2`/`ssm`/`pricing` behind a narrow interface with a fake; mount-command
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
