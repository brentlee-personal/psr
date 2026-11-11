# Internal query admission journal

This module provides native Redis/Kvrocks Lua admission and recovery bookkeeping for trusted server code. It executes no SQL and has no HTTP route, model tool, default backend observer, background retry or automatic cancellation. The proposed integration is independent of authn, sqlguard and sqlresult; none is wired here.

`NewFromNativePool(kvrocks.Get(), scope, epoch, observer)` reuses the initialized native pool through its existing `GetClient()` method. The constructor rejects nil and typed-nil pools. A nil or typed-nil observer permits bookkeeping but cannot confirm cleanup. No configuration, credential or new connection is created by this module. `New` also accepts that pool's raw Redis client for isolated fixtures.

## Fixed scope and admission

The trusted deployment fixes `Scope{Target, PolicyScope}` and an independently verified store epoch. `PolicyScope` is a stable capacity identifier, never a request ID or a policy version. Request handlers and models must not choose scopes, epochs, clients, observers, key prefixes or capacity. The key is derived from the target and stable capacity scope; changing epoch cannot select another key. Policy versions remain record data.

`Reserve(ctx, binding, deadline)` uses one Lua operation to select one of exactly two slots and create the record. There is no process-local semaphore or rate bucket. Only a successful new reservation may be considered by the future executor; a record read never authorizes execution. Duplicate execution IDs always fail, including after release and when source, owner or fingerprint changes. A lost admission response leaves a blocking record and must not trigger SQL replay. Expired new requests are rejected; existing records do not expire when their deadline passes.

After a successful KV reply, admission checks the original deadline and context again. A late/cancelled reply returns no execution record and fixed `ErrAdmissionUncertain`, while preserving the possibly written slot for reconciliation. The caller must never retry or execute from that uncertain admission. This also covers a native pool whose socket I/O outlives its request context.

`Binding` contains an execution ID, source reference, policy version, approval fingerprint, owner and fence. References are bounded safe identifiers; fingerprints are 64 lowercase hex characters. These trusted IDs must never encode tokens, DSNs, SQL, result rows or raw backend errors. The fixed schema has no payload or raw-error field. Storage and observer failures return fixed errors without wrapping their source text. Zero outcome, completeness and cleanup values mean unknown.

## Backend binding and reconciliation

`BindBackend` attaches one safe opaque locator to a matching live binding. `TransferOwner` is an explicit trusted recovery action, never a lease timeout: it preserves the execution, source, locator and slot, advances the fence/version and invalidates in-flight old-owner observations. Neither operation executes or replays a query.

`Reconcile(ctx, executionID)` reloads the current record and asks the server-wired trusted `Observer` to independently determine its actual backend state. The observer must verify backend identity/ownership and report the same execution, source, policy, approval fingerprint, owner, fence, locator and record version. It must not merely echo caller-supplied terminal flags. There is no real provider implementation or default observer in this change; the fixture observer demonstrates the state machine only.

Only a matched terminal observation with confirmed cleanup, a known outcome and a safe evidence reference can atomically mark terminal and remove that record's own slot. Raw-record CAS checks the current owner/fence/version and slot ownership. Concurrent or repeated reconciliation cannot release twice or remove a later reservation. Unknown state, errors, cancellation, missing locator/observer and stale observations retain the slot. Returning from a request, closing Rows or losing a process does not prove backend termination.

## Provisioning and loss boundary

Runtime constructors never initialize or reset storage. Every admission, read and CAS checks schema `1`, the configured deployment epoch and `PTTL == -1` in the same Lua operation. Missing keys/markers, a changed epoch, a key with TTL, expiry or unavailability fail closed. Restarting a manager cannot turn these conditions into empty capacity.

Stored records are treated as untrusted serialized input: read/reconciliation reject invalid state enums or combinations, unsafe/oversized references and out-of-range counters/timestamps. A released record must be terminal with known outcome, confirmed cleanup, backend locator and evidence; live records cannot claim cleanup or completion. These checks detect those malformed records, but do not prove historical backend truth or detect a syntactically valid stale snapshot.

The unexported `provisionScript` is a one-shot primitive for an independently authorized deployment owner provisioning a confirmed fresh store and epoch. It rejects every existing key, including a key already holding that epoch. It is not callable through `Manager` and there is no public reset/delete method. The tests invoke it only to provision their own miniredis fixtures. It does not establish an external epoch authority, and must never be used automatically after eviction, flush or loss. Deployment integration and store-loss recovery remain unimplemented.

Neither a schema marker nor no-TTL tests prove durable storage. This module does not configure or guarantee Kvrocks persistence, replication, eviction policy, backups or recovery after loss. Runtime adoption requires separate evidence for those properties and for the real backend observer. A partial store rollback, restored stale snapshot or independently changed scope is outside the verified unit contract.

## Retention and validation

The hash retains at most 4096 records plus two slots and schema/epoch metadata. Once full, admission returns `ErrJournalFull`. Records have bounded fields and are never automatically deleted. There is no GC implementation; any future retention policy must explicitly protect unknown/live reservations and must not silently erase execution deduplication history. No TTL, elapsed deadline or process disappearance releases a slot.

Run `bash build.sh --test -race -v ./internal/readonly/queryjournal`. The default tests execute the actual Lua and CAS code against owned miniredis fixtures, including two independent clients and two separate test-binary processes sharing one fixture. A fresh process sees the original unknown reservations after the admitting processes exit. These are synthetic service/state-machine tests, not real Kvrocks deployment, persistence, NATS, database termination, permission, cancellation-oracle or business acceptance evidence. The official script also runs its native unit supplement.
