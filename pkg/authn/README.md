# Strict native user identity

`ragflow/pkg/authn` is an in-process Go interface for an initialized PSR runtime.
Importing this package does not initialize the runtime. There is no HTTPS
verification handler in this change. Downstream applications must first adopt
the candidate source version and wire the verifier explicitly.
Create one `authn.NewVerifier()` and keep it for all stages of a request:

```go
identity, err := verifier.VerifyUserRequest(ctx, request, authn.UserTokenPolicy{
    MaxAge: 30 * time.Minute,
})
// Stop on every error. Use identity.Snapshot() only as safe metadata.
// Before each protected stage:
err = verifier.CheckCurrentIdentity(ctx, identity)
```

Only a single Authorization Bearer header supplies the credential. This path
never falls back to API keys, beta keys, cookies, body user/tenant fields, or
request query parameters. The public API exposes no configurable identity or
key authority. Native `ExtractAccessTokenContext` remains the signature
implementation;
there is no second signature algorithm. CE strict identity uses one read-only
native DAO observation for current-token uniqueness, account state and tenant
relations. The downstream EE edition retains its existing authority path.
Ordinary `ResolveAccessToken`, login and key initialization behavior are unchanged.

Verification reads the current native signing key without creating missing
keys or using generated/default values. Backend errors and token-bearing SQL
predicates are not logged by this path. `ErrDenied` identifies a known invalid,
expired, future, replaced or revoked credential, disabled account, or known
invalid explicit signed-tenant relation. `ErrUnverified` identifies unavailable key/state
sources, missing status evidence, or missing verified active tenant. Both must
stop protected work; source failure is not a business denial or a grant.

CE requires exactly one current User for the native access token and an
explicitly ordinary account (`IsSuperuser == false`), with known active status.
The current token must also match exactly, independently of database collation.
A nonempty signed tenant binds only that tenant: its real joined membership
and active Tenant must exist, with no fallback to another tenant. The native
CE login issuer continues to sign an empty tenant. For that credential, strict
verification examines all native memberships and accepts exactly one real
joined membership with an active Tenant. Zero or multiple candidates, duplicate
rows, unknown/NULL states and joined orphan memberships return `ErrUnverified`.
Known nonjoined memberships and disabled tenants cannot become candidates. No
owner/personal default, synthetic membership or user-ID inference is used.
Unknown user or tenant status values are also unverified; an explicit admin,
disabled account, or known invalid signed-tenant relation is denied.

The current-token User, complete candidate/anomaly evidence and selected real
rows come from one SQL observation. At most two candidate rows are returned;
anomaly evidence covers the entire relevant membership set. This bounds the
returned result, not the database scan. Ordinary login/API-key resolvers and
permission checks are unchanged. EE resolver sources are absent from this tree;
CE fixture results do not establish EE behavior or build compatibility. There
is no customer organization/store mapping.

IssuedAt and native ExpiresAt come from the already authenticated native token
timestamp. MaxAge zero uses native expiry, positive values only shorten it,
and negative values are rejected. The native 24-hour deadline and customer
30-minute deadline are compared using actual timestamps; rechecking cannot
refresh issuance or extend expiry. No additional authority-specific expiry is
represented by the current native account/tenant schema.

VerifiedIdentity has private proof fields, rejects JSON restoration, and redacts
JSON and fmt output. Snapshot returns a copy of safe user/tenant/auth/time
metadata, never the ticket, internal access token, signing key or proof. Keep
identity only inside trusted request state; never pass the proof to a model or
serialize it. A snapshot, zero value, or identity from another verifier cannot
be rechecked. CheckCurrentIdentity re-verifies the original signed credential,
current key, current token row, account, native tenant and joined membership,
and compares the same identity and deadlines. An original empty-tenant token
is always rechecked against the full unique-candidate rule: adding a second
valid tenant, or replacing the original tenant with another, cannot preserve
or silently switch the proof. Recheck never treats the resolved snapshot as a
signed tenant selection. It does not look up an arbitrary user/tenant and call
that a proof. Verification and recheck are point-in-time
checks; the protected operation must follow immediately under its own access
controls. This interface does not make the subsequent operation atomic with
revocation.

Tests use synthetic signing material and owned in-memory SQLite. They exercise
the actual CE login issuer and strict resolver, bounded candidate and anomaly
evidence, explicit-ordinary-account checks, original token mode on recheck,
signature/time rejection, current-token replacement/logout, account/tenant and
membership invalidation, state/key failures, missing/cross-issuer proof, output
redaction and request fallback rejection. They do not prove live native issuance,
real revocation, EE build/resolver behavior, enterprise mapping, ACK, or customer
rollout. These remain separately recorded `NOT_RUN` until actually executed.
