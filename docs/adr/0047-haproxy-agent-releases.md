# 0047 — Independent HAProxy releases and host-owned agent upgrades

Status: accepted by the product owner, 2026-10-26.

The Go manager, ingestion role, agent, protocol and independent deployment tests
belong to `ecraela/haproxy-manager`, initially a private repository. Do not copy
Converge history, identity code, credentials or local demo state into that repo.
The Go module is `github.com/ecraela/haproxy-manager`. Keep wire package names,
database identities and stored node paths compatible across extraction.

## Release authority

Versioned tags build Linux amd64/arm64 executables and a multi-platform image.
Reproducible Go builds disable VCS stamping and include the exact release and
source revision. A signed manifest binds repository, version, protocol, state
format, executable SHA-256 digests and immutable image digest. GitHub OIDC/Cosign
signatures are verified against the exact repository/workflow/tag and issuer.
Offline installs can provision an Ed25519 public key through a separate trusted
channel; changing that trust root requires explicit local provisioning.
Checksums downloaded alongside artifacts alone are not publisher verification.

## Local lifecycle

The host administrator runs `agentctl.py`, outside the unprivileged agent. The
agent never receives Docker or systemd authority. Support local Docker Compose
and Linux/systemd first. Installation is observation-only unless the existing
local policies explicitly allow configuration/runtime/TLS actions.

Keep installation metadata/executables separate from private node state. Reuse
the original identity directory and every mount, argument and local policy.
Missing/corrupt identities fail closed during upgrades; never re-enroll as a
recovery shortcut. The current compatible state schema is 1. Rollback changes
software, without rewinding renewed certificates, ACME state or command receipts.

Verify before activation; retain the previous binary/image. Journal the pending
transition durably, restart one agent at a time, and require new successful
HAProxy scrape/export evidence from the expected executable, node and version.
On failure restore the prior release and verify it. An interrupted transaction
blocks further changes until explicit `recover`; failed recovery stays visible.
An agent-only restart leaves HAProxy serving. Serving software upgrades remain
separate and must use node withdrawal, draining and application verification.

## Console and independence

Report executable version, source revision, platform and protocol through the
existing node-authenticated control stream. Older agents omit these fields;
display unknown, never infer a version from HAProxy itself. A reported version
is not proof of release signature verification. Browser read permissions and
tenant filtering apply unchanged; installation authority stays on the host.

Converge consumes a pinned revision of the independent product. Its console and
identity federation are integration clients; Go storage/telemetry/control do
not depend on Phoenix. No HAProxy approvals are introduced. DNS publication,
delegation and cleanup retain Converge's normal reviewed DNS workflow.

A standalone self-hosted frontend/local login and an optional outbound connector
to Converge are subsequent slices. The connector must be installation-initiated,
tenant-bound, revocable and narrowly scoped; it must not expose a private manager
or export node keys. It is not a prerequisite for standalone operation.

Repository privacy does not select an open-source or commercial license. Decide
distribution terms separately, before external publication. Customer-owned
HAProxy nodes and traffic remain outside Ecraela's infrastructure.

Licensing update (2026-11-03): [ADR 0060](0060-haproxy-licensing-and-support.md)
selects AGPL-3.0-only with optional commercial licensing and paid support.
Repository and release-image visibility remain private.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
