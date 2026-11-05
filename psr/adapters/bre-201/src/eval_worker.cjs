const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const { createSidecar } = require('./sidecar');

const hash = (text) => createHash('sha256').update(text).digest('hex');
const tenantId = 'bre201';
const namespaceId = 'urn:semantic:bre201:fixture:namespace:00000000-0000-4000-8000-000000000001';
const principal = 'urn:principal:bre201:agent:reader';
const projects = {
  thingweave: 'urn:semantic:bre201:fixture:project:00000000-0000-4000-8000-000000000002',
  '3d-agent': 'urn:semantic:bre201:fixture:project:00000000-0000-4000-8000-000000000003',
};

function importCorpus(pilot, corpus) {
  const projectId = projects[pilot];
  const scope = { instanceId: 'bre182-frozen-export', tenantId, projectId };
  const adapter = createSidecar({ ...scope, namespaceId, author: principal });
  const docs = corpus.filter((doc) => doc.repo === 'brentlee-personal/' + pilot);
  const datasetId = 'frozen-' + pilot;
  const snapshot = {
    scope,
    acquisition: {
      sequence: 1,
      observedTime: '2026-11-05T12:00:00Z',
      complete: true,
      authorized: true,
    },
    datasets: { code: 0, total: 1, data: [{ id: datasetId, name: pilot }] },
    documents: [
      {
        datasetId,
        response: {
          code: 0,
          data: {
            total: docs.length,
            docs: docs.map((doc) => ({
              id: doc.document_id,
              name: doc.path,
              dataset_id: datasetId,
            })),
          },
        },
      },
    ],
    chunks: docs.map((doc) => ({
      datasetId,
      documentId: doc.document_id,
      response: {
        code: 0,
        data: {
          total: 1,
          chunks: [
            {
              id: 'excerpt',
              document_id: doc.document_id,
              dataset_id: datasetId,
              content: doc.text,
              available: true,
              positions: [],
            },
          ],
        },
      },
    })),
    sidecar: {
      documents: docs.map((doc) => ({
        datasetId,
        documentId: doc.document_id,
        readers: [principal],
        originalSha256: doc.content_sha256,
        source: {
          system: 'github',
          uri: `https://github.com/${doc.repo}/blob/${doc.commit}/${doc.path}`,
          revision: doc.commit,
          locator: doc.locator,
        },
      })),
    },
  };
  adapter.applySnapshot(snapshot);
  const context = { tenantId, projectId, principal };
  return {
    adapter,
    context,
    views: new Map(adapter.current(context).map((view) => [view.native.documentId, view])),
  };
}

function predict(mode, baselineDirectory, requests) {
  const { retrieve, loadCorpus } = require(path.join(baselineDirectory, 'retriever.cjs'));
  const corpus = loadCorpus();
  const imports =
    mode === 'adapter'
      ? Object.fromEntries(
          Object.keys(projects).map((pilot) => [pilot, importCorpus(pilot, corpus)]),
        )
      : {};
  return requests.map((request) => {
    assert.deepEqual(Object.keys(request).sort(), ['allowedPilots', 'query']);
    const selected = retrieve(request.query, request.allowedPilots, 3);
    const evidenceRefs = selected.map((doc) => {
      const reference = {
        document_id: doc.document_id,
        repo: doc.repo,
        commit: doc.commit,
        path: doc.path,
        content_sha256: doc.content_sha256,
        locator: doc.locator,
      };
      if (mode === 'adapter') {
        const { adapter, context, views } = imports[doc.repo.split('/')[1]];
        const view = views.get(doc.document_id);
        assert(view, 'Every retrieved source must exist in the current authorized overlay');
        const assertion = view.records.find((record) => record.kind === 'assertion');
        const evidence = view.records.find((record) => record.kind === 'evidence');
        assert.equal(assertion.value.value, doc.text);
        assert.equal(evidence.content_sha256, hash(doc.text));
        assert.equal(evidence.source_anchor.revision, doc.commit);
        assert.equal(evidence.source_anchor.locator, doc.locator);
        assert.equal(assertion.status, 'proposed');
        assert(adapter.resolveCitation(context, { ...view.citation, mode: 'current' }));
        reference.overlay = {
          source_id: view.sourceId,
          assertion_id: assertion.id,
          evidence_id: evidence.id,
          status: assertion.status,
          excerpt_sha256: evidence.content_sha256,
        };
      }
      return reference;
    });
    return {
      label: 'unknown',
      answer:
        'Unknown. This retrieval-only system returns source excerpts and does not adjudicate the requested conclusion. Retrieved contracts do not establish exhaustive external consumers, crawler outcomes, or current connectivity.',
      impact_targets: [],
      evidence_refs: evidenceRefs,
      system_revision: mode === 'adapter' ? 'bre201-fixture-sidecar-v1' : 'bre182-retriever-v1.0.0',
      timing_ms: null,
      model_tokens: 0,
      provider_cost_usd: 0,
      failure: null,
    };
  });
}

if (require.main === module) {
  const [mode, baselineDirectory] = process.argv.slice(2);
  assert(['baseline', 'adapter'].includes(mode));
  const requests = JSON.parse(fs.readFileSync(0, 'utf8'));
  process.stdout.write(JSON.stringify(predict(mode, baselineDirectory, requests)));
}

module.exports = { predict };
