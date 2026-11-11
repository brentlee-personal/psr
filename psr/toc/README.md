# ToC local snapshot tool

`psr/cmd/psrtoc` is an early Go self-use slice for recording complete, caller-provided observations. It preserves prior snapshots, computes content hashes, and supports current, observation-time, and diff reads. Missing records in a complete snapshot are treated as removals; incomplete snapshots are rejected.

Build and run it from the repository root:

```sh
go run ./psr/cmd/psrtoc capture --store "$HOME/.local/share/psrtoc/ledger.json" --input snapshot.json
go run ./psr/cmd/psrtoc current --store "$HOME/.local/share/psrtoc/ledger.json" --scope psr
go run ./psr/cmd/psrtoc as-of --store "$HOME/.local/share/psrtoc/ledger.json" --scope psr --time 2026-01-01T00:00:00Z
go run ./psr/cmd/psrtoc diff --store "$HOME/.local/share/psrtoc/ledger.json" --scope psr --from 2026-01-01T00:00:00Z --to 2026-01-02T00:00:00Z
go run ./psr/cmd/psrtoc rollback --store "$HOME/.local/share/psrtoc/ledger.json" --scope psr --to 2026-01-01T00:00:00Z --at 2026-01-03T00:00:00Z
```

The input is one JSON `Snapshot` with `scope`, RFC3339 `observed_at`, `complete: true`, and `records`. Every record needs a stable `id`, `content`, credential-free HTTP(S) `source_uri`, `source_revision`, and `anchor`; the tool computes `content_sha256`. All records remain `candidate` and unverified.

`rollback` does not delete history: it re-records the selected historical snapshot at a new observation time. `current` then returns that restored content while `as-of` can still read the intervening observation.

This tool does not fetch or authenticate Linear, Notion, GitHub, or RAGFlow sources; it is a single-writer local JSON store, not the PSR PostgreSQL semantic store or RAGFlow metadata database. It has no authorization/ACL enforcement and does not establish BRE-190 acceptance. Store only data you are authorized to retain, and do not put customer-private content in this public repository.
