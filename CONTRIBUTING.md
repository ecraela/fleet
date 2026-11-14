# Contributing

Ecraela Fleet's original code and documentation use [AGPL-3.0-only](LICENSE).
Ecraela also offers optional commercial licensing
and paid support; see [licensing and support](LICENSING.md).

For a suspected vulnerability, use the private reporting contact in
[SECURITY.md](SECURITY.md). Public issues and pull requests are for material that
is safe to disclose.

## Licensing contributions

Submit original contributions under AGPL-3.0-only and retain existing copyright,
license and attribution notices. Identify any copied code, assets or dependencies
and their upstream license so maintainers can check compatibility and notices.
Only submit material you have the right to contribute.

You retain your copyright. An AGPL contribution alone does not grant commercial
relicensing rights. Before merging an external contribution into code intended
for the commercial offering, maintainers must arrange any additional rights with
you in writing. No contributor license agreement or copyright assignment is
included here; that agreement must be settled separately before such a merge.

## Build environment

Use Go 1.26.11, Node.js 24+, pnpm 11.25.0 and Python 3.11+. Docker Engine with
Compose is needed for integration tests. Linux agent lifecycle tests additionally
use systemd on a disposable Linux host. Do not run failure tests against customer
infrastructure.

```sh
make web-install
make format check test build
```

`make check` runs Go formatting/vet, Python compilation, frontend formatting and
TypeScript checks. `make test` runs Go race tests, Python unit tests and frontend
tests. `make build` embeds the browser assets and compiles both Go executables.
SQL tests start an isolated PostgreSQL 18 cluster; install its tools or set
`POSTGRES_BIN` to their directory. CI can provide `HAPROXY_TEST_DATABASE_URL`
for a disposable database instead. The runner does not reset a development database.

`make onboarding` creates an isolated Docker project to verify enrollment and
capability reporting against already-serving nodes. Other real-proxy scenarios
are described in [development](docs/development.md) and
[recovery testing](docs/haproxy-hardening.md). Scenarios that change a shared lab
must run sequentially. The workflows under `.github/workflows/` record the Linux
acceptance checks and published-artifact qualification.

## Source layout

| Path                                                                 | Responsibility                                     |
| -------------------------------------------------------------------- | -------------------------------------------------- |
| `cmd/`                                                               | Manager and agent command-line entrypoints         |
| `internal/domain/`                                                   | Configuration, application and operation contracts |
| `internal/agent/`, `internal/dataplane/`, `internal/haproxyruntime/` | Node-local observation and changes                 |
| `internal/server/`, `internal/store/`                                | API, authorization, durable jobs and storage       |
| `internal/telemetry/`, `internal/accesslog/`                         | Bounded metrics and request collection             |
| `web/src/`, `web/ui/`                                                | Standalone console and included UI components      |
| `proto/`, `internal/controlv1/`                                      | Control protocol and generated Go bindings         |
| `scripts/`, `deploy/local/`                                          | Installers, recovery tooling and disposable labs   |

## Changes

Keep the manager, agent, storage and local console usable without Converge.
Converge integration is optional. Do not rename persistent identities, metric
names or protocol fields as a cosmetic cleanup; older agents and saved state
must remain compatible. See [architecture decisions](docs/README.md#architecture).

Authorization, bounded input checks and recovery guards at network or disk
boundaries are intentional. Changes to those paths need tests for malformed
input, tenant isolation and interrupted operations. Never commit live keys,
credentials, node state or unsanitized diagnostic output.

Use the existing Go package boundaries, strict TypeScript and React Aria controls.
Frontend editing uses drawers. Components and semantic theme tokens live in
`web/ui/`; the component layer has no Converge package dependency.

Generated protobuf files are checked in. Regenerate them with
`make generate-control` using protoc 36.2; that target installs pinned generators.
Do not hand-edit generated output. Record system-wide behavior changes in
`docs/adr/` and update the relevant operator guide when commands or policies change.

After adding source files or changing dependency locks, run `make legal-update`
and review the source-file list, dependency licenses and original notice texts.
Ordinary builds check this inventory and generate the local source offer without
silently updating the reviewed notices. See [distribution notices](docs/third-party.md).
