'use strict';
const { clone, canonical, hash, id } = require('./mapper');
function required(value, message) {
  if (!value) throw Error(message);
}
function validObservationTime(value) {
  if (typeof value !== 'string') return false;
  const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,3}))?Z$/.exec(value);
  if (!match || !Number.isFinite(Date.parse(value))) return false;
  return new Date(value).toISOString() === `${match[1]}.${(match[2] || '').padEnd(3, '0')}Z`;
}
function normalize(config, input) {
  const s = clone(input);
  required(
    s &&
      canonical(s.scope) ===
        canonical({
          instanceId: config.instanceId,
          tenantId: config.tenantId,
          projectId: config.projectId,
        }),
    'snapshot scope',
  );
  const a = s.acquisition;
  required(
    a?.complete === true &&
      a.authorized === true &&
      Number.isSafeInteger(a.sequence) &&
      a.sequence > 0 &&
      validObservationTime(a.observedTime),
    'complete authorized acquisition required',
  );
  required(
    s.datasets?.code === 0 &&
      Array.isArray(s.datasets.data) &&
      s.datasets.total === s.datasets.data.length,
    'dataset scan incomplete',
  );
  required(
    Array.isArray(s.documents) && Array.isArray(s.chunks) && Array.isArray(s.sidecar?.documents),
    'scan arrays required',
  );
  const unique = (xs, key) => {
    const m = new Map();
    for (const x of xs) {
      const k = key(x);
      required(!m.has(k), 'duplicate identity');
      m.set(k, x);
    }
    return m;
  };
  const datasets = unique(s.datasets.data, (x) => {
    required(typeof x.id === 'string' && x.id, 'dataset id');
    return x.id;
  });
  const docsScans = unique(s.documents, (x) => x.datasetId);
  required(datasets.size === docsScans.size, 'document scope incomplete');
  const chunkScans = unique(s.chunks, (x) => canonical([x.datasetId, x.documentId]));
  const metadata = unique(s.sidecar.documents, (x) => canonical([x.datasetId, x.documentId]));
  const items = [];
  let documentCount = 0;
  for (const dataset of datasets.values()) {
    const scan = docsScans.get(dataset.id)?.response;
    required(
      scan?.code === 0 &&
        Array.isArray(scan.data?.docs) &&
        scan.data.total === scan.data.docs.length,
      'document scan incomplete',
    );
    const docs = unique(scan.data.docs, (x) => x.id);
    for (const doc of docs.values()) {
      documentCount++;
      required(
        typeof doc.id === 'string' &&
          doc.id &&
          typeof doc.name === 'string' &&
          doc.name &&
          (!doc.dataset_id || doc.dataset_id === dataset.id),
        'document parent',
      );
      const key = canonical([dataset.id, doc.id]);
      const meta = metadata.get(key);
      const response = chunkScans.get(key)?.response;
      required(
        meta &&
          Array.isArray(meta.readers) &&
          new Set(meta.readers).size === meta.readers.length &&
          meta.readers.every(
            (p) =>
              typeof p === 'string' &&
              new RegExp(
                `^urn:principal:${config.tenantId}:(human|agent|group):[a-zA-Z0-9_-]+$`,
              ).test(p),
          ),
        'trusted policy required',
      );
      const source = meta.source;
      required(
        source &&
          ['document', 'github'].includes(source.system) &&
          typeof source.uri === 'string' &&
          typeof source.revision === 'string' &&
          source.revision &&
          typeof source.locator === 'string' &&
          source.locator,
        'source provenance',
      );
      required(
        /^[\x21-\x7e]+$/.test(source.uri) &&
          !/["<>\\^`{|}]/.test(source.uri) &&
          !/%(?![a-fA-F0-9]{2})/.test(source.uri),
        'source URI',
      );
      try {
        const parsed = new URL(source.uri);
        required(
          !/[#[\]]/.test(parsed.pathname + parsed.search + parsed.hash.slice(1)),
          'source URI',
        );
      } catch {
        throw Error('source URI');
      }
      required(
        source.system !== 'github' || /^[a-fA-F0-9]{40}$/.test(source.revision),
        'github full SHA required',
      );
      required(
        meta.originalSha256 === undefined ||
          (typeof meta.originalSha256 === 'string' && /^[a-f0-9]{64}$/.test(meta.originalSha256)),
        'original file hash',
      );
      required(
        response?.code === 0 &&
          Array.isArray(response.data?.chunks) &&
          response.data.total === response.data.chunks.length,
        'chunk scan incomplete',
      );
      const chunks = unique(response.data.chunks, (x) => x.id);
      for (const chunk of chunks.values()) {
        required(
          typeof chunk.id === 'string' &&
            chunk.id &&
            chunk.dataset_id === dataset.id &&
            chunk.document_id === doc.id &&
            typeof chunk.content === 'string' &&
            typeof chunk.available === 'boolean' &&
            Array.isArray(chunk.positions) &&
            chunk.positions.every(
              (position) =>
                Array.isArray(position) &&
                position.length === 5 &&
                position.every(Number.isSafeInteger),
            ),
          'chunk parent or shape',
        );
        if (!chunk.available) continue;
        const native = { datasetId: dataset.id, documentId: doc.id, chunkId: chunk.id };
        const sourceId = id(
          config,
          'knowledge-object',
          canonical([
            config.instanceId,
            config.tenantId,
            config.projectId,
            dataset.id,
            doc.id,
            chunk.id,
          ]),
        );
        const locator =
          source.system === 'github'
            ? source.locator
            : `${source.locator};chunk=${encodeURIComponent(chunk.id)};positions=${canonical(chunk.positions)}`;
        const anchor = {
          system: source.system,
          uri: source.uri,
          revision: source.revision,
          locator,
        };
        const item = {
          sourceId,
          native,
          title: doc.name,
          content: chunk.content,
          anchor,
          readers: meta.readers,
          originalSha256: meta.originalSha256 ?? null,
        };
        item.fingerprint = hash(
          canonical({ ...item, originalSha256: meta.originalSha256 ?? null }),
        );
        items.push(item);
      }
    }
  }
  required(
    chunkScans.size === documentCount && metadata.size === documentCount,
    'unexpected document scope',
  );
  return { items, sequence: a.sequence, time: a.observedTime, digest: hash(canonical(s)) };
}
module.exports = { normalize };
