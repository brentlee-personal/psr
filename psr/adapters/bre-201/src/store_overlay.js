'use strict';
const { clone, base, generation } = require('./mapper');
function createStore(config) {
  let state = {
    sequence: 0,
    digest: null,
    time: null,
    current: new Map(),
    history: new Map(),
    changes: [],
  };
  const inScope = (c) => c?.tenantId === config.tenantId && c?.projectId === config.projectId;
  const allowed = (c, g) =>
    inScope(c) &&
    state.current.get(g.sourceId)?.readers.includes(c.principal) &&
    g.readers.includes(c.principal);
  function apply(candidate) {
    if (candidate.sequence < state.sequence) throw Error('stale sequence');
    if (candidate.sequence === state.sequence) {
      if (candidate.digest !== state.digest) throw Error('sequence replay conflict');
      return {
        sequence: state.sequence,
        digest: state.digest,
        changed: false,
        proposed: 0,
        deprecated: 0,
      };
    }
    if (state.time && Date.parse(candidate.time) <= Date.parse(state.time))
      throw Error('observation time must increase');
    const next = {
      ...state,
      current: new Map(),
      history: new Map([...state.history].map(([k, v]) => [k, [...v]])),
      changes: [...state.changes],
    };
    let proposed = 0,
      deprecated = 0;
    function changesFor(old, fresh) {
      const operations = [];
      if (old)
        for (const record of old.records) {
          const retired = {
            ...clone(record),
            revision: record.revision + 1,
            status: 'deprecated',
            observed_time: candidate.time,
            valid_time: { ...record.valid_time, end: candidate.time },
          };
          operations.push({
            op: 'transition',
            target_id: record.id,
            expected_revision: record.revision,
            from_status: 'proposed',
            to_status: 'deprecated',
            record: retired,
          });
          deprecated++;
        }
      if (fresh)
        for (const record of fresh.records) {
          operations.push({ op: 'propose', record: clone(record) });
          proposed++;
        }
      const readers =
        old && fresh
          ? old.readers.filter((p) => fresh.readers.includes(p))
          : (fresh || old).readers;
      const sourceId = (fresh || old).sourceId;
      const record = {
        ...base(config, 'change-set', `${sourceId}:${candidate.sequence}`, candidate.time, readers),
        author: config.author,
        idempotency_key: `${candidate.digest}:${sourceId}`,
        operations,
      };
      next.changes.push({ sourceId, readers, record });
    }
    for (const item of candidate.items) {
      const old = state.current.get(item.sourceId);
      if (old?.fingerprint === item.fingerprint) {
        next.current.set(item.sourceId, old);
        continue;
      }
      const history = next.history.get(item.sourceId) || [];
      const fresh = generation(config, item, history.length + 1, candidate.time);
      history.push(fresh);
      next.history.set(item.sourceId, history);
      next.current.set(item.sourceId, fresh);
      changesFor(old, fresh);
    }
    for (const old of state.current.values())
      if (!next.current.has(old.sourceId)) changesFor(old, null);
    next.time = candidate.time;
    next.sequence = candidate.sequence;
    next.digest = candidate.digest;
    state = next;
    return {
      sequence: state.sequence,
      digest: state.digest,
      changed: proposed + deprecated > 0,
      proposed,
      deprecated,
    };
  }
  const view = (g) => {
    const { fingerprint, readers, ...result } = g;
    return clone(result);
  };
  function read(context, recordId, revision) {
    const groups =
      revision === undefined ? [...state.current.values()] : [...state.history.values()].flat();
    for (const g of groups)
      if (allowed(context, g)) {
        const record = g.records.find(
          (r) => r.id === recordId && (revision === undefined || revision === r.revision),
        );
        if (record) return clone(record);
      }
    return null;
  }
  return Object.freeze({
    apply,
    current: (c) => [...state.current.values()].filter((g) => allowed(c, g)).map(view),
    history: (c, sourceId) =>
      (state.history.get(sourceId) || []).filter((g) => allowed(c, g)).map(view),
    read,
    resolveCitation(c, citation) {
      if (!citation || !['current', 'historical'].includes(citation.mode ?? 'current')) return null;
      const groups =
        citation.mode === 'historical'
          ? [...state.history.values()].flat()
          : [...state.current.values()];
      const g = groups.find(
        (g) =>
          allowed(c, g) &&
          g.records.some((r) => r.id === citation.recordId && r.revision === citation.revision),
      );
      return g ? view(g) : null;
    },
    changeSets: (c) => state.changes.filter((x) => allowed(c, x)).map((x) => clone(x.record)),
  });
}
module.exports = { createStore };
