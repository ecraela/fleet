# Security

## Report a vulnerability

Email [security@ecraela.com](mailto:security@ecraela.com), the security contact published by
[Ecraela](https://ecraela.com/security). Keep vulnerability details out of public
issues and pull requests while a report is being investigated.

For sensitive attachments, use the encryption key linked from that page and
verify its complete fingerprint there. Do not include passwords, enrollment
tokens, session cookies, private keys or unsanitized customer requests in an
unencrypted message. Reporting does not require a Converge account.

Include:

- The release tag, source revision or image digest, and the affected component.
- The deployment shape and permissions needed to reproduce the problem.
- Minimal reproduction steps, the expected control and the observed result.
- The impact and any sanitized logs or configuration needed to understand it.

Use a disposable environment for reproduction. Do not probe customer systems or
Ecraela production services without separate authorization.

## Release status

Ecraela Fleet is in release-candidate development. This public source line starts
from the previously qualified private HAProxy Manager v0.1.0-rc.14 implementation.
The new repository has independent commit and release identities; the earlier
qualification is not a qualification of a new Fleet release.

Development branches are not qualified releases. Use an exact qualified Fleet
tag and its verification results when installing or reproducing a report. See
[current status](README.md#status) and [release verification](docs/agent-releases.md).
Reports against earlier versions are still useful when the issue may affect the
current source. RC.13 failed published-asset qualification and remains unqualified.

Current source uses [AGPL-3.0-only with optional commercial licensing](LICENSING.md).
Fleet release verification identifies publisher `ecraela/fleet`. Earlier private
artifacts retain their original publisher and require their original verification
path; changing repositories does not silently rotate an installation's trust root.

## System and trust boundaries

The manager, ingestion service, embedded console, local login, agent, installers,
release tooling and backup/restore paths are in scope. Converge connectivity is
optional. Customer HAProxy nodes serve application traffic; it does not pass
through the manager or connector.

Evaluate remote clients, authenticated users, enrolled agents and a linked
management service according to the authority each actually has. A valid login
or node certificate does not grant access to another organization or node.
Manager instructions remain subject to the node's independently configured local
policy. Host administrators control their own installation and its granted
capabilities; identify any additional authority a reported issue would confer.

The properties we protect include:

- Permission and organization/node checks before reads, writes or credential use.
- Node-local checks of configuration intent, retained rollback artifacts, TLS
  paths, request collection and traffic-policy grants before applying changes.
- Isolation of malformed or excessive node reports and work reservations from
  healthy nodes and other organizations; bounded parsing, storage and work queues.
- Preserved identities and keys, authenticated backups, fenced recovery and
  health checks before a withdrawn node rejoins traffic.
- Exact release version, digest and publisher verification before installer or
  upgrade code executes. The agent has no Docker/systemd upgrade authority.
- Separation of private state from logs, telemetry, diagnostics and release assets.

HAProxy changes use permissions, local policy and audit history without an
approval workflow. Converge DNS changes retain their separate review flow.

## Assessment context

Describe the reachable entry point, attacker-controlled input, failed control,
prerequisites and concrete confidentiality, integrity or availability impact.
Tests, fixtures and examples are relevant when they expose a real broken boundary;
a filename alone does not establish that a credential is harmless.

Public keys, identifiers, secret-store references and demonstrated disposable
fixture credentials are not production secrets by themselves. Scanner matches
need context. There are no blanket finding-class exclusions or accepted-risk
exceptions in this policy.

## Operational limits

Ubuntu 24.04 with Docker Engine/Compose is the qualified manager pilot. Native
Linux agent installation is also tested. Demo results are not capacity guarantees.
Keep node state private and follow the [installation and recovery guide](docs/self-hosted-installation.md).

RC.12 and later require node-local observation bindings for raw configuration
rollback. Older unbound snapshots remain on disk but cannot be restored without
a binding. Back up the complete private agent state; do not re-enroll to work around a missing binding.

Sigstore signing publishes workflow identity and artifact digests to transparency
services even when downloadable assets remain private. See
[release verification](docs/agent-releases.md) and [publication readiness](docs/publication.md).

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
