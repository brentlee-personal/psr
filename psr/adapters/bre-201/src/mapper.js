'use strict';
const { createHash } = require('node:crypto');
const clone = (value) => structuredClone(value);
function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object')
    return `{${Object.keys(value)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${canonical(value[k])}`)
      .join(',')}}`;
  return JSON.stringify(value);
}
const hash = (value) => createHash('sha256').update(value).digest('hex');
function id(config, kind, key) {
  const h = hash(key).slice(0, 32);
  return `urn:semantic:${config.tenantId}:${config.namespaceId.split(':')[3]}:${kind}:${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
function base(config, kind, key, time, readers) {
  return {
    id: id(config, kind, key),
    kind: kind.replaceAll('-', '_'),
    tenant_id: config.tenantId,
    namespace_id: config.namespaceId,
    project_id: config.projectId,
    revision: 1,
    status: 'proposed',
    valid_time: { start: time, end: null },
    observed_time: time,
    permissions: {
      readers: [...readers],
      proposers: [config.author],
      verifiers: [],
      deprecators: [config.author],
    },
  };
}
function generation(config, item, number, time) {
  const key = `${item.sourceId}:${number}`;
  const object = {
    ...base(config, 'knowledge-object', key, time, item.readers),
    object_type: 'ragflow_chunk',
    title: item.title,
    assertion_ids: [],
    relation_ids: [],
  };
  const evidence = {
    ...base(config, 'evidence', key, time, item.readers),
    source_anchor: item.anchor,
    content_sha256: hash(item.content),
  };
  const assertion = {
    ...base(config, 'assertion', key, time, item.readers),
    subject_id: object.id,
    predicate: 'source_excerpt',
    value: { type: 'literal', value: item.content },
    confidence: 0,
    source_anchors: [item.anchor],
    evidence_ids: [evidence.id],
  };
  object.assertion_ids.push(assertion.id);
  return {
    sourceId: item.sourceId,
    native: item.native,
    generation: number,
    originalSha256: item.originalSha256,
    records: [object, assertion, evidence],
    citation: { recordId: evidence.id, revision: 1, mode: 'current' },
    fingerprint: item.fingerprint,
    readers: item.readers,
  };
}
module.exports = { clone, canonical, hash, id, base, generation };
