# Shared Go authorization decision contract

`POST /api/v1/permissions/check` evaluates the current authenticated RAGFlow identity against one persisted resource and operation. The endpoint is behind the normal API authentication middleware; callers must send an accepted RAGFlow authorization token. The request cannot supply or override a principal.

Request:

```json
{
  "resource": {
    "kind": "dataset",
    "id": "caller-known-resource-id"
  },
  "operation": "read"
}
```

Successful checks return HTTP 200 and one of:

```json
{"status":"allowed"}
{"status":"denied"}
```

`denied` deliberately does not reveal whether the resource is missing or the identity lacks access. If the permission source cannot be read, the endpoint fails closed with HTTP 503:

```json
{"status":"unverified","code":"PERMISSION_SOURCE_UNAVAILABLE"}
```

An unauthenticated request is rejected by the normal API middleware. Invalid JSON, unknown fields (including caller-supplied identity), missing fields, unsupported resource kinds/operations, and oversized requests return HTTP 400 with `{"status":"error","code":"INVALID_REQUEST"}`. The resource check is fresh on each call; integrations must repeat it immediately before protected reads or execution and must not treat a prior `allowed` result as a durable grant.

This contract evaluates only RAGFlow's persisted permission model. A ToB adapter remains responsible for mapping an already authenticated customer subject to its authorized PSR/RAGFlow identity and for checking customer-owned grants; customer mappings, scopes, content, and credentials stay outside the PSR repository. This endpoint is not a replacement for authorization on the target resource API and does not resolve the separate PSR semantic PostgreSQL store versus RAGFlow metadata database boundary.
