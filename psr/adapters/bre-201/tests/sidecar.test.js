'use strict';
const test = require('node:test');
const a = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const Ajv = require('ajv/dist/2020');
const formats = require('ajv-formats');
const { createSidecar } = require('../src/sidecar');
const { hash } = require('../src/mapper');
const before = require('../fixtures/before.json');
const after = require('../fixtures/after.json');
const restricted = require('../fixtures/restricted.json');
const clone = (x) => structuredClone(x);
const config = {
  ...before.scope,
  namespaceId: 'urn:semantic:demo:ragflow:namespace:00000000-0000-0000-0000-000000000001',
  author: 'urn:principal:demo:agent:adapter',
};
const ctx = {
  tenantId: config.tenantId,
  projectId: config.projectId,
  principal: 'urn:principal:demo:group:team',
};
const owner = { ...ctx, principal: 'urn:principal:demo:human:restricted' };
const next = (s, n) => {
  s = clone(s);
  s.acquisition.sequence = n;
  s.acquisition.observedTime = `2026-11-05T00:${String(n).padStart(2, '0')}:00Z`;
  return s;
};
const setup = () => {
  const s = createSidecar(config);
  s.applySnapshot(before);
  return s;
};
test('all emitted records and ChangeSets validate untouched BAS schema', () => {
  const s = setup();
  s.applySnapshot(after);
  const ajv = new Ajv({ strict: false });
  formats(ajv);
  const root = process.env.BAS_ROOT || path.resolve(__dirname, '../../../../../bas-bre-201');
  const valid = ajv.compile(
    JSON.parse(fs.readFileSync(path.join(root, 'schemas/semantic-runtime/m0/bundle.schema.json'))),
  );
  for (const cs of s.changeSets(ctx)) {
    a.ok(valid(cs), JSON.stringify(valid.errors));
    for (const op of cs.operations) {
      a.ok(valid(op.record), JSON.stringify(valid.errors));
      a.notEqual(op.record.status, 'verified');
      if (op.op === 'transition') {
        a.equal(op.record.revision, op.expected_revision + 1);
        a.equal(op.to_status, 'deprecated');
      }
    }
  }
  const v = s.current(ctx)[0];
  a.equal(v.records[2].content_sha256, hash(after.chunks[0].response.data.chunks[0].content));
  a.equal(v.records[2].source_anchor.revision, 'fixture-after');
  const bad = clone(v.records[1]);
  bad.value.value = { text: 'bad' };
  a.equal(valid(bad), false);
  const verified = clone(s.changeSets(ctx)[0]);
  verified.operations[0].record.status = 'verified';
  a.equal(valid(verified), false);
});
test('same-ID rename invalidates current citation; history and edit-back generations', () => {
  const s = setup();
  const old = s.current(ctx)[0];
  s.applySnapshot(after);
  a.equal(s.current(ctx)[0].sourceId, old.sourceId);
  a.deepEqual(s.current(ctx)[0].native, old.native);
  a.equal(s.resolveCitation(ctx, old.citation), null);
  a.ok(s.resolveCitation(ctx, { ...old.citation, mode: 'historical' }));
  s.applySnapshot(next(before, 3));
  a.equal(s.history(ctx, old.sourceId).length, 3);
  a.notEqual(s.current(ctx)[0].records[0].id, old.records[0].id);
});
test('sequence digest fence and higher unchanged no-op', () => {
  const s = setup();
  a.equal(s.applySnapshot(before).changed, false);
  a.equal(s.applySnapshot(next(before, 2)).changed, false);
  a.throws(() => s.applySnapshot(before), /stale/);
  a.throws(() => s.applySnapshot(next(after, 2)), /conflict/);
  a.equal(s.history(ctx, s.current(ctx)[0].sourceId).length, 1);
});
test('ACL shrink denies all current and history; regrant does not expose never-authorized history', () => {
  const s = setup();
  const old = s.current(ctx)[0];
  s.applySnapshot(restricted);
  a.deepEqual(s.current(ctx), []);
  a.deepEqual(s.history(ctx, old.sourceId), []);
  a.deepEqual(s.changeSets(ctx), []);
  a.equal(s.read(ctx, old.records[1].id), null);
  a.equal(s.resolveCitation(ctx, { ...old.citation, mode: 'historical' }), null);
  a.equal(s.current(owner).length, 1);
  const snap = next(restricted, 4);
  snap.sidecar.documents[0].readers.push('urn:principal:demo:human:new');
  s.applySnapshot(snap);
  const newcomer = { ...ctx, principal: 'urn:principal:demo:human:new' };
  a.equal(s.history(newcomer, old.sourceId).length, 1);
  a.equal(s.read(newcomer, old.records[1].id), null);
  a.deepEqual(s.current({ ...owner, tenantId: 'other' }), []);
  a.deepEqual(s.current({ ...owner, projectId: 'other' }), []);
});
test('unavailable replacement document and dataset deletion revoke old citation/history', () => {
  for (const mode of ['unavailable', 'replace', 'document', 'dataset']) {
    const s = setup();
    const old = s.current(ctx)[0];
    const snap = next(before, 2);
    if (mode === 'unavailable') snap.chunks[0].response.data.chunks[0].available = false;
    if (mode === 'replace') snap.chunks[0].response.data.chunks[0].id = 'new';
    if (mode === 'document') {
      snap.documents[0].response.data = { total: 0, docs: [] };
      snap.chunks = [];
      snap.sidecar.documents = [];
    }
    if (mode === 'dataset') {
      snap.datasets = { code: 0, total: 0, data: [] };
      snap.documents = [];
      snap.chunks = [];
      snap.sidecar.documents = [];
    }
    s.applySnapshot(snap);
    a.equal(s.resolveCitation(ctx, old.citation), null);
    a.deepEqual(s.history(ctx, old.sourceId), []);
  }
});
test('partial denied malformed duplicates mismatched parents rejected atomically', () => {
  const mutations = [
    (s) => (s.acquisition.complete = false),
    (s) => (s.acquisition.authorized = false),
    (s) => (s.chunks[0].response.code = 403),
    (s) => (s.documents[0].response.data.total = 2),
    (s) => s.chunks[0].response.data.chunks.push(clone(s.chunks[0].response.data.chunks[0])),
    (s) => (s.chunks[0].response.data.chunks[0].document_id = 'bad'),
    (s) => (s.sidecar.documents[0].source.revision = ''),
    (s) => {
      s.sidecar.documents[0].source.system = 'github';
    },
  ];
  for (const mutate of mutations) {
    const s = setup();
    const old = s.current(ctx);
    const snap = next(before, 2);
    mutate(snap);
    a.throws(() => s.applySnapshot(snap));
    a.deepEqual(s.current(ctx), old);
    a.equal(s.applySnapshot(next(before, 2)).changed, false);
  }
});
test('input and output defensive clones; operator not implicitly reader', () => {
  const snap = clone(before);
  const s = createSidecar(config);
  s.applySnapshot(snap);
  snap.sidecar.documents[0].readers.push(config.author);
  const views = s.current(ctx);
  views[0].records[0].permissions.readers.push(config.author);
  views[0].records[1].value.value = 'corrupt';
  a.deepEqual(s.current({ ...ctx, principal: config.author }), []);
  a.equal(s.current(ctx)[0].records[1].value.value, '/privacy-policy canonical');
});
test('tuple distinguishes instance tenant project dataset', () => {
  const original = setup().current(ctx)[0].sourceId;
  for (const field of ['instance', 'tenant', 'project', 'dataset']) {
    const c = clone(config);
    const snap = clone(before);
    if (field === 'instance') c.instanceId = snap.scope.instanceId = 'another';
    if (field === 'tenant') {
      c.tenantId = snap.scope.tenantId = 'other';
      c.namespaceId = c.namespaceId.replace(':demo:', ':other:');
      c.projectId = snap.scope.projectId = c.projectId.replace(':demo:', ':other:');
      c.author = c.author.replace(':demo:', ':other:');
      snap.sidecar.documents[0].readers = snap.sidecar.documents[0].readers.map((p) =>
        p.replace(':demo:', ':other:'),
      );
    }
    if (field === 'project') c.projectId = snap.scope.projectId = c.projectId.replace(/2$/, '3');
    if (field === 'dataset') {
      snap.datasets.data[0].id = 'another';
      snap.documents[0].datasetId = 'another';
      snap.chunks[0].datasetId = 'another';
      snap.chunks[0].response.data.chunks[0].dataset_id = 'another';
      snap.sidecar.documents[0].datasetId = 'another';
    }
    const s = createSidecar(c);
    s.applySnapshot(snap);
    a.notEqual(
      s.current({
        ...ctx,
        tenantId: c.tenantId,
        projectId: c.projectId,
        principal: snap.sidecar.documents[0].readers[0],
      })[0].sourceId,
      original,
    );
  }
});
test('adding and deleting another document does not re-version unchanged source', () => {
  const s = setup();
  const old = s.current(ctx)[0];
  const snap = next(before, 2);
  snap.documents[0].response.data.docs.push({ id: 'another', name: 'Another document' });
  snap.documents[0].response.data.total++;
  const scan = clone(snap.chunks[0]);
  scan.documentId = 'another';
  scan.response.data.chunks[0].document_id = 'another';
  snap.chunks.push(scan);
  const policy = clone(snap.sidecar.documents[0]);
  policy.documentId = 'another';
  snap.sidecar.documents.push(policy);
  s.applySnapshot(snap);
  a.equal(s.current(ctx).length, 2);
  a.deepEqual(
    s.current(ctx).find((v) => v.sourceId === old.sourceId),
    old,
  );
  s.applySnapshot(next(before, 3));
  a.equal(s.current(ctx).length, 1);
  a.deepEqual(s.current(ctx)[0], old);
});
test('valid GitHub full revision retained; positions never converted into lines', () => {
  const snap = clone(before);
  snap.sidecar.documents[0].originalSha256 = hash('complete synthetic file bytes');
  const source = snap.sidecar.documents[0].source;
  source.system = 'github';
  source.revision = 'b'.repeat(40);
  source.uri = `https://github.com/example/repo/blob/${source.revision}/routes.ts`;
  source.locator = 'L12-L14';
  const s = createSidecar(config);
  s.applySnapshot(snap);
  const view = s.current(ctx)[0];
  a.equal(view.originalSha256, hash('complete synthetic file bytes'));
  a.equal(view.records[2].source_anchor.revision, 'b'.repeat(40));
  a.equal(view.records[2].source_anchor.locator, 'L12-L14');
  a.notEqual(view.originalSha256, view.records[2].content_sha256);
});
test('omitted read revision only current; explicit revision history, retirement interval and monotonic observation', () => {
  const s = setup();
  const old = s.current(ctx)[0];
  s.applySnapshot(after);
  a.equal(s.read(ctx, old.records[1].id), null);
  a.ok(s.read(ctx, old.records[1].id, 1));
  const transition = s
    .changeSets(ctx)
    .flatMap((c) => c.operations)
    .find((o) => o.op === 'transition');
  a.equal(transition.record.observed_time, after.acquisition.observedTime);
  a.equal(transition.record.valid_time.end, after.acquisition.observedTime);
  a.deepEqual(transition.record.permissions, old.records[0].permissions);
  for (const time of [before.acquisition.observedTime, after.acquisition.observedTime]) {
    const snap = next(after, 3);
    snap.acquisition.observedTime = time;
    a.throws(() => s.applySnapshot(snap), /time/);
  }
  a.equal(s.current(ctx)[0].generation, 2);
});

test('rejects invalid calendar, raw URI, principals, hashes, and position tuples atomically', () => {
  const mutations = [
    (s) => (s.acquisition.observedTime = '2027-02-30T00:00:00Z'),
    (s) => (s.sidecar.documents[0].source.uri = 'https://example.com/a b'),
    (s) => (s.sidecar.documents[0].source.uri = 'https://example.com/%GG'),
    (s) => (s.sidecar.documents[0].readers = [[ctx.principal]]),
    (s) => (s.sidecar.documents[0].originalSha256 = ['a'.repeat(64)]),
    (s) => (s.chunks[0].response.data.chunks[0].positions = [['wrong']]),
    (s) => (s.chunks[0].response.data.chunks[0].positions = [[1, 0, 1.5, 0, 10]]),
  ];
  for (const mutate of mutations) {
    const sidecar = setup();
    const original = sidecar.current(ctx);
    const snapshot = next(before, 2);
    mutate(snapshot);
    a.throws(() => sidecar.applySnapshot(snapshot));
    a.deepEqual(sidecar.current(ctx), original);
    a.equal(sidecar.applySnapshot(next(before, 2)).changed, false);
  }
});

test('rejects non-string sidecar identity and author configuration', () => {
  for (const key of ['instanceId', 'tenantId', 'namespaceId', 'projectId', 'author']) {
    a.throws(() => createSidecar({ ...config, [key]: [config[key]] }));
  }
});
