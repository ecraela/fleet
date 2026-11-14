# Standalone self-hosted console

The Go manager serves its own browser console, API and local login. No Converge
account, Converge database, cloud connection or frontend server is required in
production. HAProxy traffic continues through your load balancers if the manager
is unavailable. Local HAProxy changes use permissions and audit, with no approvals.

## Try the local two-node demo

For a fresh lab, prepare the nodes and start their independent console:

```sh
export HAPROXY_PAIR_STANDALONE=1
python3 scripts/prepare_release_lab.py
python3 scripts/standalone.py up --image converge-haproxy:local
python3 scripts/standalone.py smoke --restart --traffic-seconds 15
```

For an already-running lab, skip preparation. If its state lives in Converge's
pinned checkout, pass that directory without copying identities or keys:

```sh
python3 scripts/standalone.py up --image converge-haproxy:local --demo-root ../converge/apps/haproxy_manager
python3 scripts/standalone.py smoke --restart --traffic-seconds 15 --demo-root ../converge/apps/haproxy_manager
```

Open **http://localhost:5180**. `demo-admin` can manage HAProxy; `demo-viewer` is
read-only. The helper prints the private directory containing their password
files, without printing passwords. These are synthetic local demo accounts.
Stop the additional console with `python3 scripts/standalone.py down`. Account
and node state remain; starting it again reuses them. An existing container is
never replaced silently.

The additional manager uses the demo's dedicated PostgreSQL and metrics stores;
it does not replace nodes, agents or the existing manager. It never receives a
Converge handoff secret. `--restart` checks the same local cookie after a real
manager restart. The smoke also verifies fresh SSE frames, history, discovery,
read-only restrictions, tenant consistency, logout, and unchanged node IDs/keys.
The optional traffic interval verifies both demo apps over HTTP and trusted HTTPS.
It does not repair old failed rollouts or expired demo application certificates.

The lab preparation builds its images and configures two shared-listener
HTTP/HTTPS applications and a local DNS-01 validation authority. An existing lab
does not need to be prepared again. Docker Compose and Python 3.11+ are required.

## Enable the console on an installation

Initialize the manager's PKI and tenant identity as documented in the
[service configuration guide](api.md#service-and-agent-configuration).
Set the dedicated database URL and operator credential normally.
Add `--local-origin https://haproxy.example.net` to the manager's `serve` command
(or set `HAPROXY_LOCAL_ORIGIN`). Use a certificate trusted by your operators for
that hostname. Keep the service and API on the same browser origin.

For the repository's Compose stack (use the same service UID/GID as initialization;
for host-owned development state, set `HAPROXY_UID=$(id -u)` and
`HAPROXY_GID=$(id -g)`):

```sh
export HAPROXY_LOCAL_ORIGIN=https://localhost:18443
docker compose --env-file deploy/local/state.local/compose.env \
  -f compose.yaml -f compose.standalone.yaml up -d manager
```

The development installation CA is private; install it in your local trust store
or use a correctly trusted deployment certificate. Do not bypass browser TLS
warnings. The HTTP helper above is an explicit loopback development alternative.
In production, keep `--local-insecure-cookies` and `--local-http-listen` unset.

The browser opens at `/` (the cluster list) or `/login`, with the same-origin API at
`/api/haproxy/v1/`. Node and operator integrations retain the `/v1/` API and mTLS
control transport. A reverse proxy must preserve Origin, Host, cookies and the
streaming response; disable buffering and keep SSE connections open. Do not
expose PostgreSQL, metrics, logs, HAProxy runtime sockets or Data Plane API.

See the [console walkthrough](getting-started.md) for cluster navigation, node
enrollment, application routing and traffic investigation. **Installation settings**
in the account menu contains local access and the optional Converge connection.

## Create and recover accounts

Create a private password file using your password manager or secret store. The
file must be regular, mode 0600, contain at least 15 characters and at most 1024
password bytes. Do not place a password in command arguments, environment
variables, repository files or shell history. One terminal newline is ignored;
spaces in the password are preserved.

```sh
haproxy-manager user add \
  --username operator@example.net \
  --organization-id YOUR_INSTALLATION_ORGANIZATION_UUID \
  --role administrator \
  --password-file /run/secrets/local-admin-password
```

`HAPROXY_DATABASE_URL` supplies the dedicated database. `HAPROXY_ORGANIZATION_ID`
can supply the new user's tenant. Use the existing installation identity to see
existing nodes; a different organization intentionally has an isolated fleet.
Use `--role viewer` for a read-only account. Account provisioning needs host and
database authority, not an operator token passed to the browser.

With Compose, mount the password file read-only for this one command:

```sh
docker compose --env-file deploy/local/state.local/compose.env \
  -f compose.yaml -f compose.standalone.yaml run --rm --no-deps \
  --volume /absolute/local-admin-password:/run/secrets/local-password:ro \
  manager user add --username operator@example.net --role administrator \
  --password-file /run/secrets/local-password
```

Delete the temporary password file from the host when appropriate for your
secret-store workflow. It is not needed after the account has been created.
Existing account names are never overwritten by `user add`.

Operators can change their own password in **Settings → Local access → Change password**
drawer. A host administrator can recover an account without browser access:

```sh
haproxy-manager user reset-password --username operator@example.net \
  --password-file /run/secrets/replacement-password
haproxy-manager user disable --username departed-operator@example.net
```

A password change/reset or disable revokes every session for that account.
Sign-out revokes its current server-side session. Accounts, session digests and
authentication audit persist in the dedicated database and its backups. Keep
backups encrypted; preserve the installation PKI and each node's existing state
and keys under the [backup runbook](self-hosted-installation.md#encrypted-and-authenticated-backup).

## What the console includes

Fleet enrollment, topology, live and historical charts, application/request/path
inspection, runtime drain/restore/weights, backend edits, configuration and
application versions/rollback, rolling updates, node maintenance, HTTPS listeners,
automatic DNS-01 TLS and incident history reuse the Converge console UX.
The console has **Overview**, **Applications**, **Infrastructure**, **Activity**
and **Settings** sections. Applications, clusters and nodes have detail pages
with shareable links. Application traffic is a full page; individual requests,
editing and version history open in drawers. Cluster/time filters and inventory
search survive navigation through their URL parameters. Returning to a page
shows cached observations while refreshing, with normal freshness checks.

Backend editing defaults to a rolling cluster
update when a managed entrypoint and at least two nodes are present. Direct
single-node reload remains an explicit choice; withdraw the node yourself if
serving continuity is required.

**DNS instructions** shows the records to prepare with your DNS provider. It
never writes Converge DNS, creates DNS drafts or bypasses a provider's normal
review process. Local ACME node policy and published DNS delegation are still
required before certificate issuance. The optional
[outbound Converge connector](converge-connector.md) links this installation
without replacing local login.

## Frontend development and release builds

Node 24+, pinned pnpm 11.25.0, Go 1.26.11 and Python 3.11+ are used in CI.

```sh
make web-install
make format check test build
```

`make build` compiles the frontend into the Go manager. Docker builds it in a
separate Node stage. Signed Linux manager releases embed that same bundle; the
release gate compares both architecture image executables with the published
files before signing. A direct `go build` without `make web-build` builds the API
but reports an unavailable console bundle rather than serving a fake page.

For hot reload, enable local login for `http://localhost:5180` with explicit
insecure loopback cookies, and point Vite's proxy at the development manager's
HTTPS port (default 18445, configurable with `HAPROXY_DEV_MANAGER_URL`). Run
`pnpm --dir web dev`; do not run it while the demo
helper is already publishing port 5180. Production uses only the Go executable.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
