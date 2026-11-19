# 02 — Storage: per instance, unattached, and the full list

**Status:** ready-for-agent
**Blocked by:** 01

- [ ] The instance detail lists that instance's storage: name, size, type, mount point,
      and `ON DOWN` — `kept` for a data volume, `deleted` for the OS volume.
- [ ] The instances list ends with an `unattached storage` section: volumes attached to
      nothing, with their size and what they cost, because that is the money being
      spent for no reason.
- [ ] `v` opens a storage screen: every volume on every configured cloud, attached or
      not, with the provider named — today's dashboard volume table, moved off level 0.
- [ ] `rig volumes` is unchanged.

## Notes

Mount point is not on `provider.Volume`; it carries `MountCommand`. Either parse the
target out of it or ask the instance (`findmnt`), and say which in the comment.
