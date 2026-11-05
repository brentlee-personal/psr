# BRE-201 RAGFlow semantic adapter

This Node/CJS sidecar maps fixture RAGFlow dataset, document, and chunk exports into proposed PSR records. It retains observed source generations, invalidates old current citations, and checks current authorization before serving current or historical evidence. It has no verified-store writer and does not apply ChangeSets.

The delivery covers source mapping and fixture lifecycle behavior. The 25-case comparison is retrieval-only. Both systems abstain on answers, including 22 answerable gold cases. Fact accuracy, impact quality, and correctly scoped unknown recall require a semantic evaluator and owner adjudication. This is not M3 acceptance or a live RAGFlow integration.

## Run the checks

Use Node 22.13 or later. The recorded run uses Node 26.4.0. Use a BAS checkout containing the unchanged BRE-182 v1.0.0 freeze and BRE-175 schemas. The default is sibling `bas-bre-201`.

```sh
cd psr/adapters/bre-201
npm ci --ignore-scripts
export BAS_ROOT=/Users/libin/repos/worktrees/bas-bre-201
npm test
npm run demo
npm run compare -- "$BAS_ROOT"
```

`npm test` checks schema records, lifecycle, authorization, acquisition failures, and the frozen comparison. Runtime code uses only Node built-ins. Ajv and formats are development dependencies for the untouched BAS bundle. No service starts, network API calls, port bindings, or verified writes occur during these commands. `demo` and `compare` write only their JSON evidence files.

`npm run demo` replays synthetic privacy-route fixtures, then restricts readers and deletes the source. The before and after texts use the same native document and chunk IDs. This is not a claim that these fixture revisions occurred in ThingWeave. [demo.json](evidence/demo.json) records old-reference rejection, authorized history, ACL denial, deletion denial, and the pre-revocation proposals.

## Map native exports to proposed records

| Input or observation | PSR output or adapter policy |
| --- | --- |
| Instance, owning tenant, project, dataset, document, chunk IDs | A deterministic stable `sourceId` in the adapter view. Semantic generation IDs differ from this stable source identity. Names never select identity. |
| Document name and available source chunk | A KnowledgeObject per chunk, with the document title and assertion links. Empty documents produce no assertion. |
| Exact UTF-8 chunk content | An Assertion with `predicate=source_excerpt`, scalar literal text, confidence zero, and status proposed. The adapter does not extract or verify business facts. |
| Trusted source URI, source revision, locator | Assertion source anchors and Evidence source anchor. GitHub provenance preserves the full commit and exact frozen line locator. Document anchors retain native chunk and positions observations without inventing lines. |
| Chunk bytes | `Evidence.content_sha256` hashes the excerpt. `originalSha256` in the adapter view separately preserves a supplied whole-file hash. It is metadata, not a hash verified against unavailable original file bytes. |
| Trusted acquisition sequence and observed time | Replay fence and observation time. Record revisions start at one; retirement operations increment them to two. An adapter generation counter identifies successive source observations. Source revision strings remain in the anchors. |
| Complete changed snapshot | A proposed ChangeSet containing new proposed records and proposed-to-deprecated transitions for prior records. Retirement closes the observed interval and preserves prior permissions. No verified transition exists. |
| Trusted effective reader export | PSR readers and the current private authorization gate. Principal roles, tenant mapping, acquisition authorization, and group membership must come from the trusted host. Native me/team fields alone cannot supply PSR roles. |
| Missing or unavailable source in a complete snapshot | Current citation invalidation and overlay retirement. Ordinary history access is denied after deletion. Private audit data remains in memory. |
| Permission shrink | Current and historical reads, citation resolution, and ChangeSet payload access check the latest readers and the historical readers. New access does not grant old history that the principal never held. |

All records carry the required tenant, namespace, project, integer revision, status, time, and permissions fields. The output schema rejects extra metadata fields. Native identifiers and acquisition details therefore remain in the adapter view. Schema validation covers individual records and nested ChangeSets. It does not prove a complete externally resolvable namespace/project graph or BAS governance authorization.

## Use the local interface

```js
const { createSidecar } = require('./src/sidecar');
const before = require('./fixtures/before.json');
const adapter = createSidecar({
  ...before.scope,
  namespaceId: 'urn:semantic:demo:ragflow:namespace:00000000-0000-0000-0000-000000000001',
  author: 'urn:principal:demo:agent:sidecar'
});
adapter.applySnapshot(before);
const context = {
  tenantId: before.scope.tenantId,
  projectId: before.scope.projectId,
  principal: 'urn:principal:demo:group:team'
};
const [view] = adapter.current(context);
adapter.resolveCitation(context, view.citation);
adapter.history(context, view.sourceId);
```

`read(context, recordId)` reads current records only. Passing an explicit record revision requests historical access. `resolveCitation` defaults to current mode; historical resolution requires `mode: 'historical'`. `changeSets(context)` filters payloads by the current and historical policy. Snapshot receipts contain counts and acquisition metadata only. The trusted host supplies authenticated context; callers must not accept arbitrary user-supplied principals as authentication.

`ragflow_client_stub.js` validates and normalizes fixture envelopes. It does not implement an HTTP client. API responses live under `datasets`, `documents`, and `chunks`. `acquisition`, `scope`, and `sidecar` are explicitly sidecar metadata. The stub requires complete counts, exact scope, unique identities, parent bindings, source provenance, and a trusted reader policy. It rejects failed or partial acquisitions before any state change.

The snapshot covers the configured instance, tenant, and project. A complete empty dataset list withdraws that whole scope. A partial scan must never set `complete=true`. Observation timestamps use valid UTC calendar dates with a `Z` suffix and optional millisecond precision. Raw source URIs must use valid ASCII URL syntax and percent escapes. Higher sequences require later observation times. Replaying the identical sequence and digest is a no-op. Reusing a sequence with different bytes, moving backwards, or moving observation time backwards fails. The store is process-local; restart persistence and live pagination are outside this demo.

## Reuse the frozen evaluation

The evaluator imports the existing BAS `harness.cjs` to validate frozen bytes. It sends only query and allowed pilot scope to each child process. Gold answers, labels, case IDs, notes, and expected evidence never enter system requests. Node's permission model allows child reads only from this adapter's source directory and the exact frozen retriever and corpus files. Negative probes confirm that gold, manifest, and harness reads fail. This is a fixture file-access boundary, not an operating-system isolation claim for hostile code.

Both systems use the unchanged `rag_baseline/retriever.cjs`, top three results, identical corpus bytes, pilot scope, and static revision schedule. The adapter imports all 38 excerpts into proposed records and checks content/anchor round trips for retrieved evidence. Historical commits stay distinct; ingesting the frozen corpus does not pretend that file order is a live revision timeline. Original whole-file hashes remain original reference metadata.

[comparison.json](evidence/comparison.json) retains all 25 predictions and evidence identities, input hashes, implementation hashes, known-case abstentions, intentional unknowns, and retrieval-hit diagnostics. Hits are not fact accuracy. The baseline has no current-citation invalidation, historical-read, or ACL-replay API. Its absence is reported as unsupported, not as a measured baseline lifecycle failure. Synthetic lifecycle tests are separate from the static frozen run.

## Design choice and remaining gaps

Two read-only design investigations compared an immutable event ledger with a complete snapshot-diff registry. The independent cross-judge selected the registry. It has one state owner and no additional event-ordering protocol. The chosen design retains immutable generations from the ledger candidate and requires both current and historical ACL checks. The Model the Domain principle determined the single private registry. Prove It Works determined the public-interface replay and file-access probes. Sequence Work into Verifiable Units determined core verification before the comparison and handoff.

[gaps.md](evidence/gaps.md) cites the pinned API and service owners, including existing native knowledge compilation drift detection and deletion cleanup. [fork_patch_verdict.md](evidence/fork_patch_verdict.md) records `no` for this fixture demonstration. Live acquisition, native compiled artifacts, durable recovery, real membership synchronization, and production deletion propagation still need evidence. [DELIVERY.md](DELIVERY.md) links both PRs and tested revisions.
