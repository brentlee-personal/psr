# BRE-201 coding delivery

Linear scope is [BRE-201](https://linear.app/brentlee/issue/BRE-201), mirrored by [BAS #121](https://github.com/brentlee-personal/background-agent-system/issues/121). Center base is `63ec3bc56d463bc5053c357b743ceedffee485ca` on `psr/baseline-v0.27.3`. BAS source base is `da29e3a9d1e62d2dba016021e9ba2e4a66d51c2c`.

| Repository | PR | PR-creation head SHA |
| --- | --- | --- |
| Center | [psr #5](https://github.com/brentlee-personal/psr/pull/5) | `5c5da18f98a03bbf1ab074fb00141b2f9b075804` |
| BAS | [background-agent-system #135](https://github.com/brentlee-personal/background-agent-system/pull/135) | `1f3bff5358a5db83962772069a39c97126223cf8` |

These are the published code/evidence and handoff heads at PR creation. The following commit only records delivery links and guard observations. A commit cannot embed its own hash; resolve the latest PR head with `gh pr view 5 --repo brentlee-personal/psr --json headRefOid` and `gh pr view 135 --repo brentlee-personal/background-agent-system --json headRefOid`. Final stdout reports those latest heads. Both PRs are open, non-draft, unmerged, with no auto-merge request.

This file records a fixture coding delivery. It does not record merge approval, Linear Done, verified admission, or production service acceptance.

Before publication, the remote center target advanced to `6dc829aaae6ceabd860d8a040ec67a047f36bbdc` with NOTICE, reproduction, and lock metadata changes. Remote BAS main advanced to `97fc1f298c01c6c7a9ef3f4903043bbd767cb9cf` with BRE-205 lock metadata changes. The remote comparisons show no overlap with this adapter or handoff. This delivery retains its source-assessment base and targets the requested branch names. It does not alter the other workstream's files or pins.

| Acceptance artifact | Pointer and result |
| --- | --- |
| Minimal external adapter | [README](README.md), four runtime modules in `src/`, and three fixture snapshots. No upstream business files changed. |
| Local regression | [regression.log](evidence/regression.log), 16 tests passed with zero skips. Includes untouched BAS bundle validation, stable IDs, source hashes, revision/citation lifecycle, atomic acquisition rejection, current and historical ACL gates, and gold-read denial. |
| Same frozen gold comparison | [comparison.json](evidence/comparison.json), all 25 cases retained, 25 identical ranked-evidence selections, 33 of 50 expected evidence references retrieved by each system. This is retrieval coverage, not fact accuracy. All answers abstain; 22 known cases remain unanswered. |
| Native gaps | [gaps.md](evidence/gaps.md), pinned API/service source evidence, including native knowledge compilation and deletion cleanup. |
| Fork decision | [fork_patch_verdict.md](evidence/fork_patch_verdict.md), `no` for the fixture demonstration only. |
| Runnable update scenario | [demo.json](evidence/demo.json), old current citation rejected, two authorized historical generations after rename, revoked reader denied, deleted source denied. |
| Protected inputs | [freeze_integrity.json](evidence/freeze_integrity.json), 16 frozen input/schema files unchanged and CENTER.lock unchanged. |
| G0 and Plumb | BAS `Docs/handoffs/bre-201/g0-plumb-guard.txt` records before and after checks. |

The comparison preserves `BRE182-021`, `BRE182-022`, and `BRE182-025` as unknown with citations. Semantic unknown correctness is pending owner review. No live RAGFlow, compiled-artifact replay, real identity synchronization, durable recovery, or BAS governance apply ran. The store is an in-memory proposal overlay.

Independent review used gpt-6-astra. Its four boundary findings were reproduced and fixed before the final 16-test run. The comment review found no code comments to remove. The architecture cross-judge used gpt-6-luna and selected a complete scoped snapshot registry over an event ledger.

Do not merge from coding delivery — await 评测 + 主理
