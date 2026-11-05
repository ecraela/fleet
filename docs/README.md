# Ecraela Fleet documentation

## Install and operate

Use an exact [Fleet release](https://github.com/ecraela/fleet/releases) with a
passing published-assets qualification record. See the
[repository transition](migration-to-fleet.md) for the existing-installation boundary.

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
- [Historical release notes](releases/) from the private predecessor.
- [Load, failure and recovery drills](haproxy-hardening.md).
- [Publication readiness](publication.md).
- [Third-party components and notices](third-party.md).
- [Distribution notices and source offer](adr/0061-haproxy-distribution-notices-and-source.md).

## Architecture

Start with the [architecture overview](architecture.md) for components, traffic
ownership, connections, storage and recovery. The [development roadmap](haproxy-implementation-plan.md)
describes remaining release work.

Design records provide background for specific decisions:

- [Independent management plane](adr/0035-haproxy-management-service.md).
- [Host-owned signed upgrades](adr/0047-haproxy-agent-releases.md).
- [Node-local TLS issuance](adr/0040-haproxy-automatic-dns01-tls.md).
- [Outbound Converge connector](adr/0049-haproxy-outbound-converge-connector.md).
- [Latest observations for live metrics](adr/0053-haproxy-latest-observations.md).
- [Rollback and tenant isolation](adr/0059-haproxy-rollback-and-tenant-isolation.md).

The [full decision history](adr/) includes earlier designs; the operator guides
describe current supported behavior. The [Data Plane API contract](providers/haproxy-dataplane.md)
records the tested upstream API profile.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
