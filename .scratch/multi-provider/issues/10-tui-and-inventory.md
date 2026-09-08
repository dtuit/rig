# 10 — TUI, `types`, `images`, `config` across providers

**What to build:** The dashboard and picker name the provider they are showing, hide the
balance tile when the provider cannot report one, and render marketplace type tokens without
layout breakage; `rig types`/`rig images` headers name the provider; `rig config` prints a
multi-provider example.

**Blocked by:** 01, 02; polish after 06/08/09 land.

**Status:** ready-for-agent

- [ ] Dashboard header: `rig · <provider>`; balance tile hidden on `ErrUnsupported`.
- [ ] Picker: works with 45+ RunPod tokens and Vast's grouped rows; description column shows
      `N offers` / `id`; spot column blank when unsupported.
- [ ] `rig types` / `rig types --available` human output carries the provider in the header
      line; `--json` unchanged apart from fields already added in 02/03.
- [ ] `rig images` on Docker-image providers shows the default and configured images and a
      hint that any image reference is accepted.
- [ ] `rig config` example includes `providers:` blocks for all four with the documented keys.

## Notes

Spec: ../spec.md — "Type/availability displays". Keep ADR-0002: JSON renders existing structs.

## Comments
