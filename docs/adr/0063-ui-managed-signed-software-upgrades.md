# ADR 0063 — Desired signed releases and host-owned upgrades

Status: accepted for implementation, 2026-11-09.

## Decision

Fleet records an operator's **desired exact release** separately from the
software each process reports. The console presents a publisher-verified release
catalog, the observed manager/ingest/agent versions, the desired version, the
retained previous version, HAProxy's independently observed version, and a
per-host rollout result. A release appearing in an upstream feed is only a
candidate: it becomes selectable after verifying its signed manifest, exact
publisher identity, digests, platform and compatibility metadata. Offline
installations can import a bundle signed by their independently provisioned
publisher key. Neither an arbitrary URL nor a mutable image tag is an upgrade
target.

The manager persists each request with tenant, actor, idempotency key, exact
manifest digest, target hosts, previous observed releases and audit history.
The host's **separate, locally installed updater** polls only its own desired
state, independently verifies the publisher and artifacts, checks local disk and
installation prerequisites, then invokes `agentctl.py` or `selfhostctl.py`.
The manager never receives host credentials or a general shell endpoint. The
agent retains its unprivileged service account and never gets a Docker socket,
systemd authority, or the ability to replace itself. A disconnected host keeps
serving and reconciles when it reconnects. A lost manager does not turn a
request into an unauthenticated host instruction.

## Traffic and availability

An **agent-only** upgrade restarts only the agent. HAProxy and its data-plane
rules continue in their existing process; telemetry and management from that
node may pause briefly. Fleet must not report this as a guarantee of application
availability: an already unhealthy HAProxy or an installation that couples the
agent and proxy lifecycle needs separate attention. An HAProxy binary/config
upgrade uses the existing withdrawal, drain and verification workflow and is
never hidden inside the software-upgrade action.

The manager and ingest roles use an external host supervisor. Their upgrade
pauses control, collection and the console while they restart, but does not
stop HAProxy. The UI must show that interruption before scheduling it. In a
multi-manager installation, upgrade only one compatible replica at a time; a
single-manager installation has a visible management interruption. Updater
status and retained signed bundles remain on the host when the manager is down.

## Compatibility and ordering

A signed release declares the protocol and state format it implements and the
**inclusive range** of manager and agent releases it supports. New managers
must accept the currently installed agent cohort before a rolling manager
upgrade is offered; otherwise the UI blocks the plan before any process stops.
The manager rejects a target that would strand any connected or known node.
The host updater repeats this preflight against the locally verified manifest
and current peer version. Missing, stale or unparseable compatibility evidence
is a blocked plan, not a guess based on matching major versions. Existing
protocol-1 releases without a signed range require an explicitly qualified
legacy compatibility entry; the UI does not infer an unlimited range.

When a transition needs both sides changed, publish an overlapping release:
upgrade manager to a version that speaks old and new protocols, upgrade agents
one at a time with a canary, then retire old-protocol support in a later
release. Do not publish a release that requires an atomic fleet-wide switch.
The release qualification record tests both orders and the mixed-version
window. Storage migrations are separately qualified, forward-compatible
transitions; software rollback never rewinds identity, journals or schema.

## Rollout and recovery

The plan shows eligible and blocked hosts, current/target version, trust root,
compatibility, readiness, likely control-plane interruption and rollback
availability. A node request moves through queued, downloading, verifying,
activating, checking, succeeded, rolled back or needs attention. A repeated
idempotency key returns the same request. Another active change for that host
is rejected. Agent rollout is sequential: one canary, then one host at a time,
with fresh post-upgrade discovery and HAProxy scrape before the next host.
Unknown outcome, insufficient resources or declining health pauses the rollout.

`agentctl.py` and `selfhostctl.py` retain their durable pending marker and
previous signed release. Failed activation automatically restores and verifies
that release. Recovery after an interruption uses the retained local bundle;
the manager reports the outcome when it reconnects. Only lost host trust,
corrupt identity or unavailable local rollback artifacts require manual host
repair. The UI must explain that state accurately instead of silently trying
another version.

Fleet exposes one authorization and inventory API to both the standalone
console and Converge's adapter. Converge can request a version using the same
tenant-bound API; it neither runs the updater nor stores node/manager host
credentials. Release catalog and update state remain useful without Converge.

## Delivery gates

1. Signed release compatibility metadata and mixed-version qualification.
2. Durable tenant-scoped desired state, audit, idempotency and read-only host
   polling/reporting contract.
3. Narrow host-owned agent updater with local resource preflight, signed
   activation and recovery; sequential two-node rollout under live traffic.
4. External manager/ingest updater with explicit management interruption and
   compatible rollback.
5. Standalone console catalog, plan, progress and inventory; Converge adapter
   to the same API, with permission and disconnect tests.

Issue [#9](https://github.com/ecraela/fleet/issues/9) tracks the complete
feature. No checkbox is complete until its behavior is exercised through a
real host upgrade and rollback.
