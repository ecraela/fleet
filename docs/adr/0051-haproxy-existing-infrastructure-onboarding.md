# ADR 0051: Onboard existing HAProxy without taking over its configuration

Status: Accepted

## Decision

Begin with an observation-only agent beside each existing HAProxy instance.
Keep the current application configuration and customer entrypoint in place.
Installation requires local host authority; application backends need no agents.
The existing enrollment flow remains single-use and identity-preserving.

Add a bounded, read-only `haproxy-agent preflight` command. It uses the configured
metrics endpoint, explicit configuration files and local Runtime/DPA sockets.
It never enrolls, creates state or rollback artifacts, changes permissions,
starts services, performs DPA transactions, or reloads HAProxy. Run it as the
actual service user with the same mounts and network. An administrator's check
does not establish that an unprivileged agent has the same access.

Six allowlisted checks travel on the existing agent-initiated control stream:
metrics, runtime discovery, files, configuration ownership, runtime management
and persistent management. Reports contain only fixed codes and statuses.
Paths, raw responses, configuration contents and credentials remain local.
Old agents omit the additive protobuf field; old managers ignore it. No schema
migration, new management endpoint or protocol identity change is needed.
Both consoles show report freshness and local remediation in a setup drawer.
Diagnostics never replace account permissions or existing operation verification.

The host administrator may declare `--configuration-owner external` or
`--configuration-owner manager`. External ownership rejects configuration-write
opt-in but permits monitoring and separately granted runtime operations.
Bounded header scans identify common automation comments; absence is not proof
of exclusive ownership. An automation hint blocks persistent commands unless
the host explicitly declares manager ownership. Arbitrary filesystem writers
cannot be fenced by heuristics or Data Plane API version checks.

Existing explicit configuration-control grants continue working without the new
declaration when no automation hint is present. Their ownership remains visibly
unconfirmed. New deployments should resolve other writers and declare ownership.
Runtime capability checks use read-only `show cli level`; supported runtime
operations require an admin socket as well as the existing local grant.

## Verification

The isolated `make onboarding` scenario starts two proxies with a pre-existing
externally managed configuration and two applications behind one entrypoint
before installing a manager. It checks preflight without enrollment, bad metrics,
an operator-only socket, enrollment, local login without Converge, explicit
runtime enablement without configuration adoption, and manager outages.
Continuous traffic, configuration hashes, original proxy processes and agent
identities must survive. The scenario manages only its own random Compose project.

The Runtime API check follows the official
[show cli level reference](https://www.haproxy.com/documentation/haproxy-runtime-api/reference/show-cli-level/),
checked 2026-10-28. Existing DPA reads use the pinned
[3.4 adapter contract](../providers/haproxy-dataplane.md).

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
