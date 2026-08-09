# Attic

Code that was written, worked well enough to keep, and is not currently wired into anything.

Files here are parked with a `.parked` suffix so the Go toolchain ignores them: they are not
compiled, not tested and not reachable from the running engine. They are kept because re-deriving
them would be expensive and because the approach may be worth revisiting — not because anything
depends on them.

**If you are looking for how the code works today, nothing in this directory is an answer.**

## `planetary-dense-aligner/`

A dense (per-pixel optical-flow) aligner for planetary lucky imaging, an alternative to the
alignment-point grid the mode actually uses (`internal/planetary`). It was set aside when the
canonical median-field geometry plus a dense AP grid proved both simpler and better on real Moon
and planet captures — see [../modes/planetary.md](../modes/planetary.md) for the shipped approach.

Reviving it means restoring the `.parked` suffixes, reconciling it with the current
`planetary.Options`, and re-validating against the same clips the current aligner is judged on.
