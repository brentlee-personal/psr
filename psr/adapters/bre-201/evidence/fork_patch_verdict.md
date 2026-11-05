# Fork patch verdict

`fork_patch_required = no` for the BRE-201 fixture demonstration.

The external adapter can preserve native source identities, hash mutable chunks, retain observed generations, emit proposed KnowledgeObject, Assertion, Evidence, and ChangeSet records, invalidate stale current citations, and gate current and historical reads with the latest fixture ACL. None of these operations requires a change to RAGFlow business code or a verified store.

The replayable evidence is `npm test`, `npm run demo`, and `npm run compare -- /path/to/background-agent-system`, documented in the adapter README. `regression.log`, `demo.json`, and `comparison.json` record this delivery's runs. The comparison uses the exact BRE-182 frozen corpus, retriever, and 25 gold queries. It does not claim a fact-accuracy improvement.

The source assessment in [gaps.md](gaps.md) confirms that native IDs, chunk export, deletion, ACL, and compilation freshness checks already exist. The missing PSR lifecycle and proposal logic can live outside those owners. A diff restricted to `psr/adapters/bre-201/` is additional evidence that the demo did not require an upstream patch.

This verdict does not establish that an unmodified live RAGFlow deployment meets a production PSR contract. Live acquisition, coherent multi-page snapshots, durable recovery, trusted identity and membership synchronization, real deletion propagation, native compiled-product replay, and BAS governance admission remain untested or unimplemented. A later production assessment must gather those observations before deciding whether an upstream change feed, revision export, or anchor extension earns a patch. No speculative patch is included in this delivery.
