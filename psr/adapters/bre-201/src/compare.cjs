const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const { spawnSync, execFileSync } = require('node:child_process');

const adapterDirectory = path.resolve(__dirname, '..');
const sha256 = (value) => createHash('sha256').update(value).digest('hex');
const sourceKey = (ref) =>
  JSON.stringify([ref.repo, ref.commit, ref.path, ref.content_sha256, ref.locator]);

function runWorker(mode, evaluationDirectory, requests) {
  const baselineDirectory = path.join(evaluationDirectory, 'rag_baseline');
  const permissions = [
    '--permission',
    `--allow-fs-read=${__dirname}`,
    `--allow-fs-read=${path.join(baselineDirectory, 'retriever.cjs')}`,
    `--allow-fs-read=${path.join(baselineDirectory, 'corpus.jsonl')}`,
  ];
  const result = spawnSync(
    process.execPath,
    [...permissions, path.join(__dirname, 'eval_worker.cjs'), mode, baselineDirectory],
    {
      input: JSON.stringify(requests),
      encoding: 'utf8',
      maxBuffer: 16 * 1024 * 1024,
      env: { PATH: process.env.PATH },
      timeout: 30000,
    },
  );
  assert.equal(result.status, 0, result.stderr || String(result.error));
  return JSON.parse(result.stdout);
}

function checkGoldIsolation(evaluationDirectory) {
  const deniedFiles = ['dataset/gold_cases.jsonl', 'dataset/manifest.json', 'harness.cjs'].map(
    (file) => path.join(evaluationDirectory, file),
  );
  const code =
    "const fs=require('node:fs');const results=process.argv.slice(1).map(file=>{try{fs.readFileSync(file);return false;}catch(error){return error.code==='ERR_ACCESS_DENIED';}});process.stdout.write(JSON.stringify(results));";
  const result = spawnSync(
    process.execPath,
    [
      '--permission',
      `--allow-fs-read=${__dirname}`,
      `--allow-fs-read=${path.join(evaluationDirectory, 'rag_baseline/retriever.cjs')}`,
      `--allow-fs-read=${path.join(evaluationDirectory, 'rag_baseline/corpus.jsonl')}`,
      '-e',
      code,
      ...deniedFiles,
    ],
    {
      encoding: 'utf8',
      env: { PATH: process.env.PATH },
      timeout: 30000,
    },
  );
  assert.equal(result.status, 0, result.stderr || String(result.error));
  assert.deepEqual(
    JSON.parse(result.stdout),
    [true, true, true],
    'SUT cannot read gold, manifest, or evaluator',
  );
  return {
    mechanism:
      'Node --permission exact source/retriever read allowlist; no child-process, FFI, or write grants',
    denied_files: deniedFiles.map((file) => path.relative(evaluationDirectory, file)),
    all_denied: true,
    boundary:
      'Fixture process file-access check, not hostile-code OS isolation or production authorization',
  };
}

function compare(basDirectory) {
  const evaluationDirectory = path.join(basDirectory, 'Docs/semantic-runtime/m0/eval');
  const { loadFreeze } = require(path.join(evaluationDirectory, 'harness.cjs'));
  const { manifest, cases } = loadFreeze();
  const isolation = checkGoldIsolation(evaluationDirectory);
  const requests = cases.map((item) => ({
    query: item.query,
    allowedPilots: item.pilot === 'cross' ? ['3d-agent', 'thingweave'] : [item.pilot],
  }));
  const baseline = runWorker('baseline', evaluationDirectory, requests);
  const adapter = runWorker('adapter', evaluationDirectory, requests);
  assert.equal(baseline.length, cases.length);
  assert.equal(adapter.length, cases.length);
  const rows = cases.map((item, index) => {
    const expected = new Set(item.evidence_refs.map(sourceKey));
    const diagnostics = (prediction) => ({
      retrieved_count: prediction.evidence_refs.length,
      expected_evidence_hits: prediction.evidence_refs.filter((ref) => expected.has(sourceKey(ref)))
        .length,
      expected_evidence_count: expected.size,
      scope_violations: prediction.evidence_refs.filter(
        (ref) => !requests[index].allowedPilots.includes(ref.repo.split('/')[1]),
      ).length,
    });
    assert.deepEqual(
      adapter[index].evidence_refs.map(sourceKey),
      baseline[index].evidence_refs.map(sourceKey),
      'Adapter must preserve frozen retrieval selection and identity',
    );
    return {
      case_id: item.id,
      expected_label: item.expected_label,
      baseline: baseline[index],
      adapter: adapter[index],
      diagnostics: { baseline: diagnostics(baseline[index]), adapter: diagnostics(adapter[index]) },
    };
  });
  const knownAbstentions = rows
    .filter((row) => row.expected_label === 'known' && row.adapter.label === 'unknown')
    .map((row) => row.case_id);
  const unknowns = rows
    .filter((row) => row.expected_label === 'unknown')
    .map((row) => ({
      case_id: row.case_id,
      baseline_label: row.baseline.label,
      adapter_label: row.adapter.label,
      adapter_citations_present: row.adapter.evidence_refs.length > 0,
      semantic_adjudication: 'pending owner review',
    }));
  assert.deepEqual(
    unknowns.map((row) => row.case_id),
    manifest.unknown_case_ids,
  );
  assert(unknowns.every((row) => row.adapter_label === 'unknown' && row.adapter_citations_present));
  const totals = rows.reduce(
    (sum, row) => {
      sum.expected_evidence_hits += row.diagnostics.adapter.expected_evidence_hits;
      sum.expected_evidence_count += row.diagnostics.adapter.expected_evidence_count;
      sum.retrieved_count += row.diagnostics.adapter.retrieved_count;
      sum.scope_violations +=
        row.diagnostics.adapter.scope_violations + row.diagnostics.baseline.scope_violations;
      return sum;
    },
    {
      expected_evidence_hits: 0,
      expected_evidence_count: 0,
      retrieved_count: 0,
      scope_violations: 0,
    },
  );
  assert.equal(totals.scope_violations, 0);
  const implementationHashes = {};
  for (const file of fs
    .readdirSync(__dirname)
    .filter((file) => /\.(js|cjs)$/.test(file))
    .sort())
    implementationHashes['src/' + file] = sha256(fs.readFileSync(path.join(__dirname, file)));
  return {
    schema: 'bre201.fixture-comparison/v1',
    mode: 'offline retrieval and proposed-evidence adapter comparison',
    dataset_id: manifest.dataset_id,
    dataset_version: manifest.version,
    case_count: cases.length,
    bas_source_head: execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: basDirectory,
      encoding: 'utf8',
    }).trim(),
    frozen_hashes: {
      ...manifest.content_hashes,
      'dataset/manifest.json': sha256(
        fs.readFileSync(path.join(evaluationDirectory, 'dataset/manifest.json')),
      ),
      'harness.cjs': sha256(fs.readFileSync(path.join(evaluationDirectory, 'harness.cjs'))),
    },
    implementation_sha256: implementationHashes,
    configuration: {
      retrieval_limit: 3,
      source_excerpt_count: 38,
      selection: 'unmodified BRE-182 retriever for both systems',
      revision_schedule: 'identical static freeze; synthetic lifecycle replay is a separate demo',
      provider: null,
      node: process.version,
    },
    isolation,
    retrieval_diagnostics: {
      ...totals,
      identical_ranked_evidence_cases: rows.length,
      fact_accuracy: null,
      impact_recall: null,
      impact_precision: null,
    },
    answers: {
      baseline_unknown_count: baseline.filter((row) => row.label === 'unknown').length,
      adapter_unknown_count: adapter.filter((row) => row.label === 'unknown').length,
      known_case_abstentions: knownAbstentions,
      known_case_answer_successes: 0,
      unknown_cases: unknowns,
      unsupported_certainty_on_unknown: 0,
      current_fact_accuracy: null,
      correct_unknown_recall: null,
      reason:
        'Neither system generates or semantically adjudicates answers. Known abstentions are failures to answer. Unknown labels plus citations are a safety check, not adjudicated correct-unknown recall.',
    },
    lifecycle: {
      adapter_evidence: 'demo.json and regression.log',
      baseline_current_reference_invalidation: 'unsupported API',
      baseline_historical_read_api: 'unsupported API',
      baseline_acl_replay: 'unsupported API',
      fact_improvement_claim: false,
    },
    fork_patch_required: 'no',
    fork_patch_scope: 'fixture demonstration only',
    m3_comparison_run: false,
    owner_adjudication: 'pending',
    verified_store_writes: 0,
    usage: { model_tokens: 0, provider_cost_usd: 0, cpu_cost: null, review_cost: null },
    rows,
  };
}

if (require.main === module) {
  const basDirectory = path.resolve(
    process.argv[2] ||
      process.env.BAS_ROOT ||
      path.join(adapterDirectory, '../../../../bas-bre-201'),
  );
  const result = compare(basDirectory);
  fs.writeFileSync(
    path.join(adapterDirectory, 'evidence/comparison.json'),
    JSON.stringify(result, null, 2) + '\n',
  );
  console.log(
    JSON.stringify({
      cases: result.case_count,
      identical_ranked_evidence_cases: result.retrieval_diagnostics.identical_ranked_evidence_cases,
      known_case_abstentions: result.answers.known_case_abstentions.length,
      gold_read_denied: result.isolation.all_denied,
      m3_comparison_run: false,
    }),
  );
}

module.exports = { compare, runWorker, checkGoldIsolation };
