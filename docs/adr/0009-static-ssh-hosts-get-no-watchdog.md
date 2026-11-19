# Static ssh hosts are lent, not rented: no watchdog, no lifecycle, key-only

rig's machines have always been rented: created by `up`, self-stopped by the TTL watchdog,
deleted by `down`. The `ssh` provider adds machines rig is lent — a shared office GPU box listed
under `providers.ssh.hosts` — and every one of those three verbs would be wrong there. The
watchdog's `shutdown -h now` would take a shared machine down for everyone, and installing its
systemd unit needs root rig does not have. `down` would claim to delete something rig never
created.

So the driver reports `Capabilities.Static`, and core treats it as one switch: `up`, `down` and
`extend` refuse with `ErrUnsupported`, `adopt` installs only the `~/.rig` layout
(`Footprint.NoWatchdog`) and writes no ledger event, the dashboard's create picker leaves the
provider out, and the host's configured user wins over the top-level `ssh.user` (which exists to
override cloud images' login users). Everything ssh-shaped is unchanged.

We chose this over a self-stop line that does nothing (`SelfStop: "true"`): the watchdog would
still need root under systemd, would still show a deadline that means nothing, and `extend`
would still appear to work.

Login is key-only. Password auth was considered and rejected: rig runs ssh without a terminal,
supervised tunnels reconnect unattended, and the only workaround (`sshpass`/`SSH_ASKPASS`) means
storing a domain password where rig can read it. `ssh-copy-id` with the password once is the
supported setup.
