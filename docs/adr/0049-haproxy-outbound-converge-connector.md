# ADR 0049: Optional outbound Converge connector

Status: accepted for the self-hosted pilot (2026-10-27).

Self-hosted installations retain their independent Go manager, local accounts,
storage, agents and data plane. Connecting an installation to Converge must not
require publishing a manager port or giving Phoenix access to telemetry. HAProxy
changes use permissions, local policy, verification and audit, with no approvals.
DNS drafts and provider writes retain Converge's existing review flow.

## Decision

Add an optional `relay` role to the same Go executable. Deploy that role beside
Converge with its own HAProxy service database and HTTPS endpoint. Converge's
same-origin `/api/haproxy` proxy points at this relay. Phoenix remains the issuer
of short-lived, tenant-bound browser grants, including its existing permission,
billing, live-session and recent-MFA checks. Browser sessions stay at the relay;
neither Converge cookies nor installation operator tokens reach the other side.

A Converge operator with `haproxy.manage` creates a named link and a ten-minute
random pairing code. A local administrator pastes that code into the standalone
connection drawer and explicitly chooses observation or management access. The
code binds the cloud organization and link; it cannot select a local tenant.
The local session selects that tenant. The first successful claim binds a random
installation connector credential. Pairing retries can only retain the same
credential, never replace it. Revoked and expired unclaimed codes fail closed.

The manager persists its credential in a private `0600` file, locks the private
state directory against multiple owners, then initiates a TLS-verified HTTP/2
gRPC stream to its installation-configured relay origin. Persist before claim
and reuse the identity after a lost response or restart. A corrupt state file or
changed relay does not generate a replacement identity. No node/ACME private key,
password, operator token or cloud signing secret is used as this credential.

The version-one stream carries an allowlist of HAProxy browser requests. It
cannot open TCP connections, execute commands, expose local login, forward
operator credentials or reach enrollment/ingestion/agent identity APIs. The
manager dispatches requests in process through the same authorized handlers and
existing agent policies; it does not dial a supplied URL. The linked local
organization is fixed by local consent. The relay validates the cloud cookie,
capability, origin and CSRF first; the manager validates the bounded request
identity and intersects management capability with local link policy. Actor IDs
continue into the existing operation audit. Organization fields in API responses
are structurally translated to the cloud organization; customer strings and
64-bit counter values are preserved.

The relay holds no copy of historical metrics, configurations or node state.
Live SSE responses are flushed across the stream without a polling store-and-
forward delay. DNS readiness evidence comes from the existing manager checks;
the relay binds that evidence to the cloud actor and organization and signs the
existing DNS intent. It does not call a DNS provider or grant DNS permissions.

## Bounds and failure behavior

Each relay process permits 128 connected managers and each link permits 32
concurrent requests. Frames, request bodies, response sizes, queues, query/header
lengths, claim attempts and link creation rates are bounded. Slow readers cancel
their requests; they cannot retain an unbounded response queue. Normal requests
expire within nine seconds; live requests cannot outlive the short cloud login.
The relay rechecks live browser sessions and link revocation while streaming.
Heartbeats detect dead peers within eight seconds. Reconnection uses bounded
backoff and jitter, with a fresh transport and the same persisted identity.

Neither transport reconnect nor the relay retries a mutation. A timeout or
disconnect after dispatch may follow an accepted change: inspect operation or
version history before retrying. Existing request IDs and optimistic generation
checks remain in effect. Accepted asynchronous operations continue locally even
if the cloud is disconnected. HAProxy application traffic is never on this link.

Either side can unlink. Cloud unlink revokes the stored credential and closes
the stream. Local unlink cancels requests and removes the local credential
immediately, then attempts cloud revocation. If the cloud is unavailable, revoke
its remaining link from Converge when it returns. Local sessions/accounts and
agent identities are independent and are not reset by linking, disconnecting or
restarting the relay. Link configuration, claim and revocation are audited with
allowlisted metadata and no credential material.

## Pilot limits

The first version links one manager/local organization to one Converge
organization. A manager does not link its other local organizations implicitly.
The relay connection registry is in memory: deploy one relay replica in this
pilot. Its database survives restarts; managers reconnect automatically. Multi-
relay routing, multiple managers per organization, browser administration of
local users, local MFA and enterprise IdP federation are separate work.

Keep the relay signing secret only in Phoenix and the relay, enforce verified
TLS at both ingress and outbound connection, and back up the private connector
state with the installation. Loopback insecure browser cookies are an explicit
demo setting, never a production deployment default. No public release, SaaS
deployment or licensing change is implied by this implementation.

See [the connector runbook](../converge-connector.md).

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
