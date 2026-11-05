# Ecraela Fleet

Ecraela Fleet monitors and manages HAProxy® load balancers on infrastructure you own.
It includes a Go backend, a browser console with local login, and an agent for
each HAProxy node. It works without Converge or an Ecraela account.

The manager handles enrollment, metrics, application routing and configuration
changes. Your load balancers handle application traffic and continue serving
when the manager is unavailable. Enrollment does not install HAProxy or create
a shared traffic entrypoint; you provide the nodes, application servers and
networking.

## Status

Fleet is a public source preview under [AGPL-3.0-only](LICENSE), with
[optional commercial licensing and paid support](LICENSING.md). Its initial
source snapshot comes from the previously qualified private manager release,
v0.1.0-rc.14. Fleet has a new repository and release publisher; that earlier
qualification does not qualify a new Fleet binary or image.

**No Fleet release artifacts have been published yet.** Use the source demo below
to try the console and agent. Production installation requires an exact signed
Fleet release and its qualification results. See [release verification](docs/agent-releases.md)
and the [repository transition](docs/migration-to-fleet.md).

The tested pilot shape is Ubuntu 24.04 with Docker Engine and Compose; native
Linux agents are also tested. Small-demo results are not capacity guarantees.

## What it does

- Node enrollment, configuration discovery, fleet status and live/historical metrics.
- HTTP hostname/path routing, backends, health checks and shared HTTPS listeners.
- Node-local TLS keys, DNS-01 issuance and certificate renewal.
- Runtime drain/restore and weights; persistent configuration versions and rollback.
- Rolling updates with node maintenance, health checks and automatic recovery.
- Request and path investigation, incidents and signed webhook notifications.
- Signed, pinned releases; agent upgrades and encrypted manager/node backup and restore.

Local administrator and viewer accounts work independently of the optional
[outbound Converge connector](docs/converge-connector.md). HAProxy changes require
permissions and explicit agent policy, with audit history and verification.
They have no approval workflow. DNS changes use your DNS provider's normal process.

Report vulnerabilities through the private contact in [SECURITY.md](SECURITY.md).

## Install or connect existing nodes

Use the [self-hosted installation guide](docs/self-hosted-installation.md) for
signed release installation, upgrades and recovery once a qualified Fleet
release is available. There is no default password or public signup.

For a deployment you already run, follow the
[existing HAProxy guide](docs/existing-haproxy.md). Start with monitoring and
read-only diagnostics, then explicitly grant the capabilities you want the agent
to have. Each agent needs local access to its node's metrics and, for management,
its Runtime API and Data Plane API. Application servers do not need an agent.

## Try it from source

The two-node lab needs local Docker Engine/Compose, Make and Python 3.11+.
It builds its own images, creates disposable credentials and a local TLS authority,
and publishes only loopback ports. It requires no Converge checkout or login.
Do not use the lab deployment as your production installation.

```sh
export HAPROXY_PAIR_STANDALONE=1
python3 scripts/prepare_release_lab.py
python3 scripts/standalone.py up --image converge-haproxy:local
```

Open **http://localhost:5180**. The helper creates `demo-admin` and `demo-viewer`
accounts and prints the location of their private password files. The shared
HTTP traffic entrypoint is **http://localhost:18081**. The lab includes two
applications on shared HTTP/HTTPS listeners and a local DNS-01 test authority.

```sh
python3 scripts/standalone.py smoke --restart --traffic-seconds 15
python3 scripts/pair.py failover
```

The `converge-haproxy:local` image and Compose project name are retained lab
identifiers, not external dependencies. State, identities and keys remain under
the ignored `deploy/local/state.local/` directory. To stop the lab while
preserving that state:

```sh
python3 scripts/standalone.py down
python3 scripts/pair.py shutdown
```

See [development and demo instructions](docs/development.md) for the smaller
single-node stack, individual scenarios and port details. Run mutation and
failure scenarios one at a time against disposable nodes.

## Build and test

Use Go 1.26.10, Node.js 24+, pnpm 11.25.0 and Python 3.11+. The Dockerfile pins the
build environment. All Go and frontend package dependencies are public; the UI
components are included in this repository.

```sh
make web-install
make format check test build
```

The build embeds the frontend in the Go manager and writes executables to
`.build.local/`. SQL tests use an isolated PostgreSQL 18 cluster; install its
tools or set `POSTGRES_BIN` to their directory. CI can explicitly provide
`HAPROXY_TEST_DATABASE_URL` for its disposable database. The test runner does not
reset a development database. See [contributing](CONTRIBUTING.md) for the checks
and code layout.

## Documentation

- [Documentation index](docs/README.md)
- [Licensing and paid support](LICENSING.md)
- [Applications and TLS](docs/applications.md)
- [Configuration and node operations](docs/operations.md)
- [Metrics, incidents and request investigation](docs/observability.md)
- [Node-local country and network visibility](docs/geography.md)
- [Manager and agent configuration](docs/api.md)
- [Load, failure and recovery drills](docs/haproxy-hardening.md)
- [Publication readiness](docs/publication.md)
- [Third-party components and notices](docs/third-party.md)

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
