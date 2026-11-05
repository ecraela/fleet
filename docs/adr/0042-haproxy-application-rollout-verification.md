# ADR 0042: Node-local application verification during rolling updates

Status: Accepted

Date: 2026-10-26

The user authorized application-aware rolling verification and automatic rollback
on customer-owned HAProxy nodes. Extend the existing independent Go coordinator,
agent probe runner and console. Existing `haproxy.manage`, management MFA, tenant,
CSRF and local configuration-control requirements apply. No HAProxy approval
workflow is introduced. DNS retains its ordinary review and publication flow.

## Protocol selection and local authority

Application intent gains an optional `verification: {http: bool, https: bool}`.
At least one selected protocol is required when the object is present. Absence
retains existing behavior and canonical digests. Disabled/deleted applications do
not need successful requests; their effective active baselines are verified if
compensation restores them.

The operator enables direct checks through the agent's existing local probe file
with `scope: "node"`. The local operator must point each such check at that
agent's own HAProxy instance, never a shared entrypoint. Default `entrypoint`
checks remain read-only reachability observations under ADR 0038. A healthy peer
cannot satisfy a node rollout gate. Cloud and self-hosted use the same contracts.

Hostname, frontend, route prefix, protocol, expected status and a policy digest
are safe discovery metadata. Addresses, roots, response expectations and response
content stay local. The digest covers local settings, pinned resolved destination
and trust material. A browser/manager selects protocols, not remote destinations,
trust roots, request credentials or arbitrary assertions. Existing five-second
cadence, bounded concurrency, deadlines and response limits remain in force.

Agents advertise application control version 3. Before dispatch, every target
must advertise matching node-local grants for every selected protocol of every
active verified application in both desired and rollback documents. Missing
policy, old capability or an unmanaged entrypoint rejects the deployment. Frozen
per-node check sets are durable and immutable in PostgreSQL. The generated
protobuf contract transports only bounded safe grants and results.

## Rolling gate and compensation

For each withdrawn/drained node, apply through DPA, verify runtime/configuration
health, then require two distinct passing direct observations before rejoin.
After rejoin, require two further passing observations before touching the next
node. Evidence must be fresh, produced after the phase barrier, and match the
application digest, frozen local policy, loaded artifact and runtime generation.
Replayed observations cannot earn passes. Shared listener and unrelated active
verified applications are checked as part of the effective document.

A real HTTP/HTTPS response, assertion or TLS failure immediately fails the gate.
Missing/stale evidence waits up to thirty seconds, then fails. Local policy
changes fail rather than replacing already-authorized check authority. Final
completion rechecks current evidence for previously passed nodes; completed pass
counts survive time spent rolling a peer, but stale/missing evidence never does.

Existing exclusive reservations, append-only revisions, idempotency, reverse
compensation and maintenance checkpoints remain authoritative. Restored baseline
applications must pass the same before/after rejoin gates. Failed baseline
verification while withdrawn retains a maintenance recovery reservation. A
failure detected after rollback rejoin triggers another withdrawal/drain before
handoff for operator recovery. The next node is not changed. The console keeps
deployment failure and rollback verification evidence visible together.

## Scope and evidence

These direct checks prove delivery from locally configured node-side locations.
They do not establish public reachability, DNS publication, every backend's
behavior, or a cluster-wide shared-entrypoint verification gate. Entrypoint
probes remain independently visible observations. Rich configurable phase
policies, public probes and provider VIP adapters remain roadmap work.

Memory/PostgreSQL contracts cover HTTP/TLS failure, missing observations,
post-rejoin failure, idempotency, tenancy, immutable authority, capability fencing,
and failed rollback quarantine. Probe tests use real HTTP/TLS servers, including
invalid trust. The two-node demo keeps app B under continuous HTTP/trusted-HTTPS
traffic while app A receives a healthy transport-level backend with the wrong
application response. HTTP and HTTPS failures must each stop the rollout,
reverify the baseline and preserve app B and shared listener/TLS resources.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
