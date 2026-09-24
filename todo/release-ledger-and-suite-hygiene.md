# Release-ledger and suite hygiene

## Context

Findings from an external survey of this repository, verified against the
working tree. Each item is independent. The two ledger items are worth doing
before the next release; the rest are consider-at-leisure.

## 1. Backfill release-ledger anchors

The release archives under `.rlsbl/releases/v*.toml` carry neither
`candidate_sha`/`tree_hashes` nor an `unanchorable` marker — they predate
rlsbl's ledger anchoring. Under current rlsbl, every guarded ledger read
(unreleased range, `rlsbl status`, `rlsbl unreleased`, `rlsbl release run`)
hard-errors on a missing anchor. Run the anchor backfill script from the rlsbl
repository (`scripts/backfill_release_anchors.py`), `--dry-run` first, review,
then apply and commit.

Effort: small.

## 2. Changelog coverage for commits past the last release

Commits exist past the latest release tag with no entries in
`.rlsbl/changes/unreleased.jsonl` (at filing time: the strictspec validator
regeneration and its accompanying dependency-floor bump). Add entries via
`rlsbl changelog add` (likely `--no-user-facing`) so the next release's
coverage check passes without archaeology.

Effort: trivial.

## 3. Revisit testisolation sandbox-runner adoption

`testisolation_sandbox_required` is set to `"false"` with an in-file comment
marking it as pending the sandbox runner. The suite's spend-guard architecture
has since stabilized; revisit whether the sandboxed runner can now be adopted
and the stance flipped.

Effort: small-medium.

## 4. Settle the selfdoc manifest placement

`pyproject.toml` carries a comment documenting an undecided question: selfdoc
requires a newer Python than the package's own floor, forcing a
version-marker split if declared normally. Decide the placement (marker-split
dependency, dev-group-only, or removal from the manifest) and delete the
comment.

Effort: small.
