# Ecraela Fleet documentation

## Install and operate

Fleet currently provides source; see the [repository transition](migration-to-fleet.md)
for release availability and the existing-installation boundary.

- [Install a self-hosted manager](self-hosted-installation.md): signed releases, upgrades, backup and restore.
- [Connect existing HAProxy nodes](existing-haproxy.md): prerequisites, monitoring and explicit management permissions.
- [Local console and account recovery](self-hosted-console.md).
- [Applications and TLS](applications.md): routing, verification, certificate renewal and application lifecycle.
- [Configuration and node operations](operations.md): backend edits, runtime controls, rollouts and maintenance.
- [Metrics, incidents and request investigation](observability.md).
- [Node-local country and network visibility](geography.md): trusted ingress, local datasets, coverage and request filters.
- [Manager and agent configuration and operator API](api.md).
- [Agent release installation and lifecycle](agent-releases.md).

## Optional Converge integration

- [Outbound connector](converge-connector.md): link a manager while retaining independent local login.
- [Converge development integration](converge-integration.md): shared-login development setup and DNS handoff.

## Develop and qualify

- [Licensing and paid support](../LICENSING.md).
- [Contributing](../CONTRIBUTING.md): build environment, source layout and checks.
- [Security reporting and boundaries](../SECURITY.md).
- [Development and local demos](development.md).
- [Current private v0.1.0-rc.12 release notes](releases/v0.1.0-rc.12.md).
- [Previous private v0.1.0-rc.11 release notes](releases/v0.1.0-rc.11.md).
- [Earlier release notes](releases/).
- [Load, failure and recovery drills](haproxy-hardening.md).
- [Publication readiness](publication.md).
- [Third-party components and notices](third-party.md).
- [Distribution notices and source offer](adr/0061-haproxy-distribution-notices-and-source.md).

## Architecture

[Architecture decision records](adr/) describe accepted behavior and its limits.
Their numbering continues from the original Converge integration; the number is
not a missing-file sequence in this repository. Older records describe the
system at the time of the decision. Later records supersede the corresponding
behavior.

The [implementation plan](haproxy-implementation-plan.md) contains proposed work
and implementation notes. It is not the current feature list or a production
support promise. Use the release notes and operator guides for those boundaries.
The [Data Plane API contract](providers/haproxy-dataplane.md) records the tested
upstream API profile.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
