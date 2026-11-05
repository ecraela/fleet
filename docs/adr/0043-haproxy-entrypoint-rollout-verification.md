# ADR 0043: Entrypoint request verification during rolling updates

Status: Accepted

Date: 2026-10-26

The user authorized the next delivery slice: require application requests through
the customer-owned shared entrypoint during rolling updates, alongside the
direct checks in ADR 0042. Keep the independent Go coordinator and outbound
agent control stream. Existing tenant permissions, management MFA, local control
policy and append-only history apply. HAProxy has no review or approval stage;
DNS publication, delegation and cleanup retain normal review.

## Policy and local authority

Application intent adds an optional boolean `verification.entrypoint`. When true,
each selected HTTP/HTTPS protocol needs both node and entrypoint grants on every
target agent, for every active verified application in desired and rollback
documents. Absence/false preserves existing policy and canonical digests.
Entrypoint verification cannot replace direct node verification.

Destinations, pinned addresses, request paths, trust roots and response assertions
remain exclusively in the local probe file. Operators point node grants directly
at that agent's HAProxy and entrypoint grants at the shared entrypoint. The
manager/browser can select protocols and whether the entrypoint gates deployment;
they cannot supply arbitrary fetch destinations or assertions. Existing cadence,
timeouts, concurrency, size limits and normal TLS verification remain unchanged.

Agents advertise application control version 4, which stamps both probe scopes
with the initiating agent's loaded artifact/runtime generation. This correlation
prevents reports from an old application or pre-reload batch from satisfying a
new gate. It does not identify which upstream served the shared request.
Version 3 agents remain usable for direct-only verification; entrypoint-gated
desired or rollback documents require version 4. Protobuf adds one optional flag;
the existing bounded scope/grant/result messages carry the evidence.

## Rolling gates and compensation

Freeze both scope grant sets and policy digests in the existing immutable rollout
check arrays before dispatch. While withdrawn, require two distinct fresh direct
passes. After rejoin, start a new phase barrier and require two fresh passes on
each direct and selected entrypoint check before updating the next node. Every
active verified neighbor participates. Completion checks current evidence for
previously passed nodes as well as controller state.

Results must match scope, frozen local policy, application digest, request,
loaded artifact and runtime generation and be newer than the phase barrier.
Replayed/pre-rejoin/stale/unrelated observations cannot count. Real response or
TLS failures stop immediately; missing evidence waits at most thirty seconds.
Entrypoint failure/timeout/policy-change codes are distinct from direct failures
and the console labels each check's path and preserves rollback evidence.

Use reverse compensation: withdraw/drain a rejoined node, restore its baseline,
verify it directly, rejoin and verify it directly again before moving backward.
For entrypoint-gated baselines this direct post-rejoin phase is
`after_node_restore`. Only once every changed node is restored, require two fresh
passes on both paths from every selected location in `after_cluster_restore`.
A shared request can hit a peer still awaiting compensation; gating the shared
path between individual restorations could strand a healthy recovered node and
leave the bad peer serving. No healthy peer may stand in for a direct node gate.

Failed direct baseline verification causes withdrawal and a durable maintenance
handoff as before. If all changed nodes were directly verified and the final
shared path still fails, report `rollback_entrypoint_verification_failed` and
recovery needs attention while leaving restored nodes serving. A shared failure
does not identify an unsafe node. External DNS, network, VIP or trust outages may
survive rollback; withdrawing healthy baselines does not repair those outages.

## Evidence and limits

Entrypoint gates prove the configured request contract from the named agent
locations. They do not establish public reachability, published DNS, every
upstream's entrypoint path, or exact request distribution. A peer may serve a
shared request; direct node gates remain mandatory. Read-only entrypoint probes
without opt-in policy keep their existing behavior. Public probes, configurable
phase budgets and provider entrypoint adapters remain separate roadmap work.

Memory/PostgreSQL contracts cover HTTP/TLS failures with healthy node checks,
missing evidence, version/grant fencing of both desired and baseline authority,
tenancy, idempotency, compensation, neighboring apps, direct-restoration quarantine
and shared failures during peer compensation. Real HTTP probe tests distinguish the two destinations and protocol
round trips retain the new intent. The two-node demo uses an opt-in backend
whose health/direct-check paths succeed while Store's entrypoint request path
fails. Several independent entrypoint checks exercise round-robin selection.
HTTP and HTTPS must independently stop and restore every changed node while
Admin serves continuous HTTP/trusted-HTTPS traffic and shared TLS is preserved.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
