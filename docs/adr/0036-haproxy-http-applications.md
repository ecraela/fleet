# ADR 0036: HTTP applications on managed HAProxy listeners

Status: Accepted

Date: 2026-10-25

The requested application slice extends ADR 0035 with HTTP hostname/path routing,
backend servers, HTTP health checks and rolling deploy/rollback. It applies
directly under `haproxy.manage`, local configuration policy and the existing
shared-login/MFA session. It adds no review or approval workflow.

## Ownership and deployment

The independent Go service owns tenant/cluster-bound application identities and
immutable revisions in its PostgreSQL database. A revision references its durable
rollout, actor, idempotency key and optional prior application revision. Runtime
status comes from that rollout; saving intent is distinct from verified deployment.
Memory and PostgreSQL use the same admission and reconciliation contracts.

An application targets the cluster's declared existing HTTP frontend. The first
profile supports exact ASCII DNS hostnames, literal path prefixes, one backend
pool per application, round robin/least connections/first available, and up to 16
servers. Host matching ignores case and the optional port. Longest path prefix
wins; duplicate listener/hostname/prefix ownership is rejected. Paths are forwarded
unchanged. Existing listener binds, request/response rules and fallback routing
are retained; unmatched traffic follows that existing fallback. Defining a new
listener or changing the fallback is a separate operation outside this profile.

Stable UUID-derived `cv_app_` backend names identify managed sections. A bounded
node-local description records safe application intent. The agent compares that
intent with actual backend fields, health-check path and ordered frontend rules.
Raw configuration and full rollback snapshots stay private on nodes. The protocol
reports an application-control version, and admission requires every target to
support it before any mutation is dispatched.

Inherited node-local defaults are preserved outside application intent. DPA can
report their `from` name only after a cold parse; this parser field is accepted
and retained during edits without accepting other unknown managed-section fields.

The agent composes managed backends and inline hostname/path switching rules
through the pinned DPA 3.4.5 full-section APIs in one versioned transaction. It
preserves unmanaged sections and unknown fields. A route rewrite never becomes
an arbitrary HAProxy expression, URL fetch, file path or shell command.

Application deployments initially require the supported 2–12-node cluster shape,
a healthy enrolled entrypoint, and identical unmanaged backend projections across
the target nodes. Node-specific settings outside that projection remain local.
The whole cluster and its entrypoint are fenced during each deployment. Each node
is withdrawn, drained, updated, checked and rejoined using ADR 0035's coordinator.
HTTP health checks run every second; two passing checks mark a backend ready and
two failures mark it down. HAProxy's default HTTP success range is 2xx/3xx. The
coordinator additionally requires its continuous health windows before advancing.
End-to-end requests are exercised by the demo smoke; declared topology alone is
not public reachability evidence.

## History and recovery

An explicit application rollback creates a new revision and recomposes the selected
application's prior intent with the currently observed, known revisions of other
applications. It never restores an old cluster snapshot over a newer neighbour.
The existing full-node snapshots remain valid for automatic compensation while
the rollout exclusively owns the captured artifacts. External changes, ambiguous
commands and failed drain/rejoin retain the existing attention/recovery behavior.

After a stopped partial deployment is settled and maintenance is recovered, a new
explicit deployment may reconcile different known revisions of its own application
on target nodes. Other applications and unmanaged projections must still agree.
Unknown application ownership is not adopted automatically. The generic backend
and runtime APIs cannot modify application-owned resources.

The initial bounded profile retains at most 16 applications, 64 revisions per
application and 256 revisions per cluster. Reaching a bound refuses further
changes; it does not delete recovery history. Add explicit archival before raising
these limits. The same API, agent and coordinator work without Converge through
the installation operator interface.

## Validation

`make haproxy-applications` deploys two applications on `public`, tests longest
prefix routing through both nodes, continues neighbouring-app requests during an
update and manager restart, updates the neighbouring app, then rolls the first
app back without reverting its neighbour. A failing HTTP check exercises automatic
compensation. Go/PostgreSQL and console contracts cover identity, revision conflicts,
idempotency, tenant isolation and permission/CSRF checks.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
