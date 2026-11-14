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

The verified tenant must come from the native edition resolver and must have a
current active tenant row and joined native membership. This public CE tree's
resolver discards tenant context and returns empty. Therefore CE verification
returns `ErrUnverified`; it never invents a tenant from a user ID. EE resolver
sources are absent from this tree; synthetic resolved-tenant fixtures exercise
shared checks and do not establish EE resolver behavior. Real EE verification
requires the downstream implementation, its native signed active tenant, and
separate runtime evidence. There is no customer organization/store mapping.

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
