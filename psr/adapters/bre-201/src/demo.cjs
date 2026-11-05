const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { createSidecar } = require('./sidecar');

function runDemo() {
  const fixture = (name) =>
    JSON.parse(fs.readFileSync(path.join(__dirname, '../fixtures/' + name + '.json'), 'utf8'));
  const before = fixture('before');
  const after = fixture('after');
  const restricted = fixture('restricted');
  const config = {
    ...before.scope,
    namespaceId: 'urn:semantic:demo:ragflow:namespace:00000000-0000-0000-0000-000000000001',
    author: 'urn:principal:demo:agent:sidecar',
  };
  const adapter = createSidecar(config);
  const team = {
    tenantId: config.tenantId,
    projectId: config.projectId,
    principal: 'urn:principal:demo:group:team',
  };
  const retained = { ...team, principal: 'urn:principal:demo:human:restricted' };
  const initialReceipt = adapter.applySnapshot(before);
  const original = adapter.current(team)[0];
  const updateReceipt = adapter.applySnapshot(after);
  const updated = adapter.current(team)[0];
  assert.equal(original.sourceId, updated.sourceId);
  assert.equal(adapter.resolveCitation(team, original.citation), null);
  assert.equal(adapter.read(team, original.citation.recordId), null);
  assert(adapter.resolveCitation(team, { ...original.citation, mode: 'historical' }));
  assert.equal(adapter.history(team, original.sourceId).length, 2);
  assert.equal(
    updated.records.find((record) => record.kind === 'assertion').value.value,
    '/privacy-policy redirects to /privacy 301',
  );
  const authorizedChangeSets = adapter.changeSets(team);
  const aclReceipt = adapter.applySnapshot(restricted);
  assert.equal(adapter.current(team).length, 0);
  assert.equal(adapter.history(team, original.sourceId).length, 0);
  assert.equal(adapter.changeSets(team).length, 0);
  assert.equal(adapter.resolveCitation(team, { ...original.citation, mode: 'historical' }), null);
  assert(adapter.current(retained).length > 0);
  const deleted = structuredClone(restricted);
  deleted.acquisition.sequence += 1;
  deleted.acquisition.observedTime = '2026-11-05T00:04:00Z';
  deleted.documents[0].response.data = { total: 0, docs: [] };
  deleted.chunks = [];
  deleted.sidecar.documents = [];
  const deleteReceipt = adapter.applySnapshot(deleted);
  assert.equal(adapter.current(retained).length, 0);
  assert.equal(adapter.history(retained, original.sourceId).length, 0);
  assert.equal(adapter.resolveCitation(retained, updated.citation), null);
  assert(deleteReceipt.deprecated > 0);
  return {
    scenario: 'Synthetic privacy route rename with stable native IDs, then ACL shrink and deletion',
    proof_boundary:
      'Fixture responses only. This does not replay an actual ThingWeave deployment or RAGFlow server.',
    source_id: original.sourceId,
    native_ids: original.native,
    old_citation: original.citation,
    new_citation: updated.citation,
    old_current_citation_invalid: true,
    old_current_read_denied: true,
    authorized_history_generations_after_rename: 2,
    removed_reader_current_denied: true,
    removed_reader_history_denied: true,
    removed_reader_changesets_denied: true,
    deleted_source_current_denied: true,
    deleted_source_ordinary_history_denied: true,
    receipts: {
      initial: initialReceipt,
      update: updateReceipt,
      acl: aclReceipt,
      deletion: deleteReceipt,
    },
    authorized_proposals_before_revocation: authorizedChangeSets,
    verified_store_writes: 0,
    upstream_writes: 0,
  };
}

if (require.main === module) {
  const result = runDemo();
  fs.writeFileSync(
    path.join(__dirname, '../evidence/demo.json'),
    JSON.stringify(result, null, 2) + '\n',
  );
  console.log(
    JSON.stringify({
      old_current_citation_invalid: result.old_current_citation_invalid,
      authorized_history_generations: result.authorized_history_generations_after_rename,
      revocation_denied: result.removed_reader_history_denied,
      deletion_denied: result.deleted_source_current_denied,
    }),
  );
}

module.exports = { runDemo };
