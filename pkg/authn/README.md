# Strict native user identity

`ragflow/pkg/authn` is an in-process Go interface for an initialized PSR runtime.
Importing this package does not initialize the runtime. Standalone consumers may call
`OpenRuntime(ctx, protectedConfigPath)` once to open native metadata and Kvrocks
without migrations, engines, workers or key creation. There is no HTTPS
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
key authority. Native `ExtractAccessTokenContext`, `UserDAO.GetByAccessToken`
and the edition's `resolveUserAuthTenant` remain the owning implementations;
there is no second signature algorithm. Ordinary `ResolveAccessToken`, login
and key initialization behavior are unchanged.

Verification reads the current native signing key without creating missing
keys or using generated/default values. Backend errors and token-bearing SQL
predicates are not logged by this path. `ErrDenied` identifies a known invalid,
expired, future, replaced or revoked credential, disabled account/tenant, or
inactive/missing membership. `ErrUnverified` identifies unavailable key/state
sources, missing status evidence, or missing verified active tenant. Both must
stop protected work; source failure is not a business denial or a grant.

The verified tenant is established inside the native strict verifier, never by
consumer request fields. Enterprise resolution still delegates to its edition
resolver. In CE, an explicit signed tenant must have a current valid tenant and
joined membership; it never falls back to another tenant. A CE token with empty
tenant can bind only a unique current joined/active native tenant for a known
non-superuser. Reads retain NULL/unknown states and are bounded to two candidates;
ambiguity or unknown authority yields ErrUnverified. No user, tenant or membership
is created. Ordinary login and API-key resolution are unchanged. Rechecking an
empty-tenant proof repeats uniqueness and rejects any tenant change; it cannot
turn the old proof into another tenant or refresh issuance. This is native tenant
identity, not customer organization/store mapping or a business grant.

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
and compares the same identity and deadlines. It does not look up an arbitrary
user/tenant and call that a proof. Verification and recheck are point-in-time
checks; the protected operation must follow immediately under its own access
controls. This interface does not make the subsequent operation atomic with
revocation.

Tests use synthetic signing material and owned in-memory SQLite. They cover
signature/time rejection, current-token replacement/logout, account/tenant and
membership invalidation, state/key failures, missing/cross-issuer proof, output
redaction and request fallback rejection. They do not prove real issuance,
real revocation, EE build/resolver behavior, enterprise mapping, ACK, or customer
rollout. These remain separately recorded `NOT_RUN` until actually executed.
