# ADR 0048: Standalone HAProxy console and local login

Status: accepted for the self-hosted pilot (2026-10-27).

The manager must be usable without Converge identity, a Converge proxy, network
access to Ecraela, or a JavaScript server in production. Customer application
traffic stays in the customer's data plane. HAProxy changes require permissions,
agent policy, verification and audit, with no approval workflow.

## Decision

Build the React console in this repository and embed its static bundle in the
Go manager. Docker and signed Linux manager binaries contain the same assets.
`--local-origin` enables the console and local authentication on an exact HTTPS
origin. The ingestion and agent roles never serve local login. The existing
operator API and optional Converge handoff remain compatible and independent.

Reuse the HAProxy screens and a private snapshot of the Ecraela design system.
There are no dependencies on the Converge checkout, workspace packages, services,
telemetry or identity. Editing, application creation, version history, backend
editing and traffic inspection keep their existing drawers and charts. DNS
controls become provider instructions: no standalone action writes Converge DNS
or skips its normal review flow. A future outbound connector is a separate ADR.

Local users belong to one tenant, assigned by a host administrator through the
manager CLI. The account cannot select its own organization or role during login.
`administrator` grants `haproxy.read` and `haproxy.manage`; `viewer` grants read
only. The API loads current account state for every authorized request and live
stream authorization pass. Cloud cookies cannot authorize a local session and
local cookies cannot mint cloud grants. An explicit `X-HAProxy-Session: local`
selects the local mechanism, without granting access by itself.

Passwords use the bounded Argon2id format (64 MiB, three passes, two lanes), a
random salt and constant-time comparison. Passwords have at least 15 characters,
allow spaces and Unicode, and are limited to 1024 bytes. There is no default
password or public registration. Login limits are persisted across replicas by
source and normalized identity, with a global budget and two concurrent hashing
slots per manager. Unknown users take the same hashing path. Forwarded source
headers are not trusted.

Session credentials are random, stored only as SHA-256 digests in PostgreSQL,
and sent in host-only HttpOnly, SameSite=Strict, Secure cookies. Local sessions
have a 12-hour absolute lifetime and 30-minute idle timeout. Mutations require
the exact browser origin and a session-bound CSRF token held in memory. Password
changes, CLI resets, disable and logout revoke sessions in PostgreSQL. Credential
updates and login publication serialize on the account row. Authentication audit
is append-only and contains no passwords, cookies, CSRF tokens or password hashes.

The loopback demo explicitly enables insecure development cookies and a separate
HTTP browser listener published only on 127.0.0.1. Node/control paths remain on
HTTPS. Production does not enable this listener or those cookie flags.

## Operations and limitations

Database backups include local accounts, password hashes, sessions and audit.
Existing node identities, certificates, retained configuration and local keys
remain in their existing storage. Password reset is available to a host/database
administrator even if browser login is unavailable. Keep the database backup and
installation/node state under the existing encrypted backup procedure.

The initial local user administration surface is the host CLI. MFA, external
identity providers, browser user administration and the outbound Converge
connector remain future work in this initial decision. Licensing was undecided
at the time; [ADR 0060](0060-haproxy-licensing-and-support.md) subsequently selects
AGPL-3.0-only with optional commercial licensing and paid support. The repository
stays private. See [the standalone runbook](../self-hosted-console.md).

Security references checked 2026-10-27:

- https://pkg.go.dev/golang.org/x/crypto/argon2
- https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
