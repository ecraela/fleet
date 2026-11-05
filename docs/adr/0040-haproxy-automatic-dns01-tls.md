# ADR 0040: Node-local automatic TLS with reviewed DNS delegation

Status: Accepted

Date: 2026-10-25

The user authorized automatic DNS-01 issuance and renewal on customer-owned
HAProxy nodes. Extend ADRs 0035–0039 for this capability only. HAProxy operations
remain direct, permissioned and audited; there are no HAProxy approvals. DNS
continues through import, compare, plan, approve, apply and verify.

## Authority and key ownership

The same Go manager and agent run in hosted and self-hosted deployments.
Phoenix owns neither issuance jobs nor certificate keys. A node-local
`--acme-policy-file` (or `HAPROXY_ACME_POLICY_FILE`) explicitly grants an HTTPS
ACME directory, trust roots, terms acceptance, an existing listener, exact
hostnames, a dedicated validation zone, resolver/update endpoints and RFC 2136
TSIG credentials. These settings and filesystem paths cannot be supplied by a
browser command. Configuration control and the existing local TLS binding remain
independent prerequisites. Changing local authority requires an agent restart.

Each node generates its own ECDSA account and certificate keys. Private account
state and resumable order journals live in the private agent state directory.
Immutable generation PEMs live in a locally granted directory shared with the
local HAProxy process. Give that process read access through a dedicated group;
it does not need write access to the certificate volume. Certificate/account
keys, TSIG secrets, trust files and PEM contents never enter manager storage,
Phoenix, browser responses, control messages or audit metadata. The manager
receives public SANs, leaf fingerprints, validity dates and bounded error codes.

The first solver performs HMAC-SHA256 TSIG-authenticated RFC 2136 TXT updates.
Server ACLs must grant only the exact challenge targets and TXT type. Concurrent
nodes/orders can share a target: insert individual values and delete only the
journaled value, never the RRset. Persist before presenting, clean on completion
or cancellation, and retry journal cleanup after restart. No ordinary provider
credential is needed by the agent.

## DNS delegation retains its review boundary

Use a CNAME from `_acme-challenge.<application hostname>` to an exact target in
the dedicated validation zone. The console can prepare this CNAME only from a
current, verified application and matching public grants on all target nodes.
The Go manager signs the bounded record intent using ADR 0039's short-lived,
actor/organization/cluster/application/version-bound contract. Phoenix verifies
it and prepares an ordinary DNS working version, link and audit atomically.
Existing records are preserved. This creates no change set, approval, deployment
or provider write. Publishing and verification remain in the zone's normal DNS
flow, including scoped DNS permissions and recent MFA.

Agents independently verify the actual CNAME and propagated TXT before asking
the CA to validate. A draft or UI checkbox never stands in for published DNS.
Self-hosted operators can establish the same delegation through their own DNS
process without depending on Converge or Phoenix.

## Durable issuance and renewal

A manager plan freezes the node roster, topology binding, exact application
hostnames and public local policy digests. Enabling captures explicit ongoing
renewal/rolling activation authority. Renew, retry and disable use
`haproxy.manage`, CSRF, existing management MFA and idempotency keys. Disabling
stops new issuance; a rollout already committed to the coordinator completes its
safe drain/rejoin or compensation sequence. Existing serving certificates remain
installed. Revocation or loss of manager contact fences in-flight issuance.

Agents poll their own authenticated mTLS identity endpoint on an independent
bounded worker. CA/DNS waits never block live telemetry or the control stream.
Authority has a 15-minute deadline and is rechecked every three seconds; loss of
contact for 15 seconds cancels work. All DNS and HTTP operations have deadlines
and bounded responses. CA requests stay on the locally configured HTTPS origin,
with verified trust and no redirects or environment proxy.

The manager stores plans, jobs, retry dates and history in its own PostgreSQL
schema. Reports are bound to the authenticated node and current generation.
Retries resume the same key/order, respect CA Retry-After and use bounded
exponential backoff. Five failed attempts pause for operator attention. Renewal
is scheduled at 60–69% of the shortest certificate lifetime with deterministic
jitter. Freshness outages defer scheduling; changed authority or a newer manual
listener choice stops automatic replacement and requires explicit setup again.
This first profile does not implement ACME Renewal Information (ARI).

## Rolling activation and rollback

All target nodes must have usable certificates before a listener rollout can
start. The common typed listener intent retains one logical generation reference
and a bounded map of node identity to leaf fingerprint. Each agent selects and
pins its own leaf. Legacy shared-certificate listeners remain supported.

Plan activation, listener revision creation and rollout association commit in
one cluster/entrypoint transaction. Restarting managers cannot create duplicate
rollouts. The existing coordinator withdraws, drains, updates, verifies loaded
TLS identity and backend health, and rejoins each node before proceeding. A
failure uses the existing compensation/attention path; it never overwrites
certificate bytes behind a serving alias. Existing application/backend writers
preserve the typed listener map. Restoring retained, valid listener history is a
new version and checks every node's own certificate pin again.

## Initial limits and deferred work

One automatic TLS plan per cluster frontend, 2–12 nodes, at most eight exact
hostnames; no wildcard, HTTP-01, TLS-ALPN-01, EAB, arbitrary new listeners or
provider-wide DNS API credentials. CA quotas apply separately to each node and
can be lower than these protocol bounds. Production deployments must confirm
capacity with their CA before enabling a large cohort.

The plan retains 20 generation summaries and 64 action receipts. Node storage
retains at most 128 immutable generations, including rollback material, with no
remote deletion API. At capacity issuance pauses visibly; archive unneeded local
material only after checking active and retained rollback references. The wire
inventory contains static aliases, current document references and recent issued
aliases; old assets can still be loaded by a retained local reference. Listener
revision numbers allow up to 1,000,000; the console lists the latest 64. Existing
configuration artifact retention limits still apply. Expiry/issuance failure is
visible in the console; external notifications and archival automation remain
subsequent work.

## Validation

The opt-in two-node lab uses pinned Pebble 2.10.1 and BIND 9.20.30. DNS-01
validation stays enabled and the resolver follows the real CNAME/TXT chain.
The source delegation is a private pre-existing fixture, not a public provider
write. The console separately prepares the corresponding normal DNS draft.
Test independent node keys, trusted HTTPS for both apps, missing delegation,
renewal scheduling, CA outage/backoff, manager/agent restart, rolling activation,
compensation and retained listener rollback. Public CA issuance is a separate
operator staging/production validation step, never implied by local test success.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
