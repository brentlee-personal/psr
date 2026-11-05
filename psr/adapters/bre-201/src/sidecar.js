'use strict';
const { clone } = require('./mapper');
const { normalize } = require('./ragflow_client_stub');
const { createStore } = require('./store_overlay');
function createSidecar(input) {
  const c = clone(input);
  const slug = '[a-z][a-z0-9-]*';
  const uuid = '[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}';
  if (
    !c ||
    typeof c.instanceId !== 'string' ||
    !c.instanceId ||
    typeof c.tenantId !== 'string' ||
    typeof c.namespaceId !== 'string' ||
    typeof c.projectId !== 'string' ||
    typeof c.author !== 'string' ||
    !new RegExp(`^${slug}$`).test(c.tenantId) ||
    !new RegExp(`^urn:semantic:${c.tenantId}:${slug}:namespace:${uuid}$`).test(c.namespaceId) ||
    !new RegExp(`^urn:semantic:${c.tenantId}:${c.namespaceId.split(':')[3]}:project:${uuid}$`).test(
      c.projectId,
    ) ||
    !new RegExp(`^urn:principal:${c.tenantId}:(human|agent):[a-zA-Z0-9_-]+$`).test(c.author)
  )
    throw Error('invalid sidecar configuration');
  const store = createStore(c);
  return Object.freeze({
    applySnapshot: (s) => store.apply(normalize(c, s)),
    current: store.current,
    history: store.history,
    read: store.read,
    resolveCitation: store.resolveCitation,
    changeSets: store.changeSets,
  });
}
module.exports = { createSidecar };
