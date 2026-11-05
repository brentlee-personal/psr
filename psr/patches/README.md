# `psr/patches/` — adapter patches (convention)

Empty on BRE-199 by design.

## Rules

- Prefer **sidecar / overlay / config** over patches (see `psr/STRATEGY.md`).
- If a core patch is unavoidable, add `NNNN-short-name.patch` (or a series file) and document:
  - why sidecar/extension points were insufficient
  - upstream files touched
  - which pin tag/commit it applies to
  - how to re-apply after a pin bump
- Never delete upstream `LICENSE` or invent a fake root `NOTICE` if upstream has none.
