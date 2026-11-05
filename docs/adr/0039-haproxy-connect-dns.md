# ADR 0039: Connect a verified HAProxy application to a DNS draft

Status: Accepted

Date: 2026-10-25

## Decision

Add an optional Connect DNS drawer to application details. Operators select a
Converge zone containing the application's exact hostname and supply an A, AAAA
or CNAME traffic entrypoint and TTL. Paths stay in HAProxy routing. DNS preparation
does not modify the application, listener, certificate, cluster or customer network.

The address is supplied by the operator. Existing customer-side probes establish
application readiness; they do not verify the supplied address or public internet
reachability. Never derive a DNS destination from the display-only topology URL,
Docker address or node management address.

## Service handoff

The independent Go manager exposes a management-authorized, CSRF-protected POST
on an application. It requires a Converge human identity, the exact latest
successful revision, loaded matching configuration on every active node, and fresh
passing entrypoint checks. Missing, disconnected, stale, failed or mismatched
observations cannot authorize preparation.

The manager signs a stateless intent valid for 60 seconds. It binds the actor,
organization, cluster, application, revision, hostname, zone, expected working
version, record type/value/TTL, request UUID and verification time. It reuses the
dedicated HAProxy integration key with the HMAC prefix `haproxy-dns-v1.` and a
fixed `converge-dns` audience. Login grants and DNS intents are not interchangeable.
No operator token, agent credential, certificate key or telemetry crosses to Phoenix.
The browser holds the intent only for the request; it is never placed in storage,
URLs or logs. This optional integration does not affect standalone operation.

Phoenix independently resolves current session, membership, HAProxy management,
zone/name read and management permissions and existing strong MFA. It verifies the
bounded intent again after taking the usual billing, organization and zone locks.
It preserves the locked working records, including records hidden by a scoped
role. A changed working version or conflicting owner record fails without writes.
An identical existing record can be linked without creating a redundant version.
CNAMEs at the apex or pointing to themselves are rejected.

## Persistence and DNS review

The ordinary DNS working version, a correlation link and allowlisted audit history
commit in one Phoenix transaction. Links use the request UUID for idempotency,
store a fingerprint of stable intent fields, and reference the tenant/zone/version
with a composite foreign key and forced RLS. Repeating the same request does not
create another version or audit event. Different settings under that UUID conflict.

There is no distributed transaction or Go-to-Phoenix callback. Go continues owning
application revisions and observations; Phoenix owns only DNS state and the narrow
reference to the app revision verified at preparation time. Later health changes
do not retroactively invalidate a saved DNS draft.

Preparation does not publish desired state, plan a provider change, approve, apply,
enqueue provider writes or verify public DNS. The review link opens the existing
zone draft review. DNS retains `import -> compare -> plan -> approve -> apply ->
verify`; HAProxy edits retain direct permissions, local policy and verification
without approval. Application rollback does not silently change DNS.

The drawer lists the latest accessible links, distinguishing records present in
the draft, published desired state, and drafts that changed. Published desired
state is not evidence of provider deployment or public propagation. Provider
deployment and DNS verification remain in the zone. Automatic cleanup, public
target verification and permanent management of DNS records from HAProxy are
separate work.

## Validation

Tests cover purpose/signature/expiry/identity boundaries, tenant and name scopes,
revoked authority, CSRF/MFA, stale readiness and working versions, conflicts,
idempotency, audit and unchanged published/provider state. The local two-node demo
uses a reserved `.test` zone with no provider attached to exercise the real
Go-to-Phoenix handoff and normal review drawer without external DNS writes.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
