const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { compare, runWorker } = require('../src/compare.cjs');
const { runDemo } = require('../src/demo.cjs');

const basDirectory = path.resolve(
  process.env.BAS_ROOT || path.join(__dirname, '../../../../../bas-bre-201'),
);

test('rename, current invalidation, historical read, revocation, and deletion execute through the public adapter', () => {
  const result = runDemo();
  assert.equal(result.old_current_citation_invalid, true);
  assert.equal(result.authorized_history_generations_after_rename, 2);
  assert.equal(result.removed_reader_history_denied, true);
  assert.equal(result.deleted_source_current_denied, true);
  assert.equal(result.verified_store_writes, 0);
});

test('all 25 frozen cases preserve retrieval identities and disclose all known abstentions', () => {
  const result = compare(basDirectory);
  assert.equal(result.case_count, 25);
  assert.equal(result.retrieval_diagnostics.identical_ranked_evidence_cases, 25);
  assert.equal(result.retrieval_diagnostics.scope_violations, 0);
  assert.equal(result.isolation.all_denied, true);
  assert.deepEqual(
    result.answers.unknown_cases.map((row) => row.case_id),
    ['BRE182-021', 'BRE182-022', 'BRE182-025'],
  );
  assert.equal(result.answers.known_case_abstentions.length, 22);
  assert.equal(result.answers.current_fact_accuracy, null);
  assert.equal(result.m3_comparison_run, false);
});

test('SUT rejects a request carrying evaluation labels', () => {
  assert.throws(
    () =>
      runWorker('baseline', path.join(basDirectory, 'Docs/semantic-runtime/m0/eval'), [
        { query: 'privacy', allowedPilots: ['thingweave'], expected_label: 'known' },
      ]),
    /expected_label/,
  );
});
