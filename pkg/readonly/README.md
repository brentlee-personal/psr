# Native model-external PostgreSQL execution

`Open(ctx, policyFile, connectionDirectory, nativeVerifier)` opens a separate
business connection. The directory contains the existing Secret keys `host`,
`port`, `database`, `user`, `password`, `sslmode`. It never borrows `dao.DB`, runs
migrations, creates data/roles or initializes signing keys.

`Authority` rechecks the same native proof and current protected policy, database,
session role, nonprivileged role flags, complete physical column metadata and
SELECT privileges. Functions are restricted to the approved pg_catalog list and
checked for nonvolatile/non-security-definer definitions. This policy is an
explicit isolated-source grant, not a customer/merchant permission inferred from
a catalog or profile.

`Approve` invokes the existing `internal/readonly/sqlguard.Analyze` and requires
the allowed physical relation. Approval is private, issuer-bound, single-use,
nonrestorable and binds exact SQL, policy, identity, scope, fingerprint and limits.
No model/HTTP boolean can supply it.

`Execute` invokes `internal/readonly/sqlresult.Query`, not the Agent ExeSQL tool.
One user statement runs in a native read-only transaction. Complete results are
released only after synchronous Rows completion, successful transaction rollback,
connection close and fresh identity/policy checks. Ordered columns/rows return to
the caller; this API has no model, Agent history or tool callback.

Caps are 100 rows, 1 MiB, 30 seconds and two local slots. Unknown cleanup retains
capacity and closes new admission. These local single-replica smoke mechanics do
not establish durable restart reconciliation or global multi-replica capacity.
An unknown outcome must not be cleared by blindly restarting the process.

Parser/result-reader source is reused unchanged from fixed a31cc1b3. New bridge
validation and actual fixture execution are recorded separately; historical unit
results are not a new-source CI or business-success receipt.
