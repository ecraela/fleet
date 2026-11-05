# Converge development integration

This guide is only for developers working on the optional Converge integration.
The commands in this guide run from a Converge checkout, which contains the pinned
`apps/haproxy_manager` submodule. Standalone installations do not need it.
Converge's Makefile explicitly sets `HAPROXY_PAIR_STANDALONE=0`; the independent
repository's two-node lab defaults to standalone mode.

## Converge console integration

From the repository root:

```sh
make haproxy-console
```

The wrapper sets `HAPROXY_CONVERGE_ROOT` to the Converge checkout explicitly.
When invoking `scripts/demo.sh console` directly, set that variable yourself.
This starts the single-node demo and the existing Converge development
stack, then enables shared login using two optional Compose overlays. It creates
a separate private `state.local/browser-login` signing secret and mounts that
same file into Phoenix and the Go manager. The console connects directly to
`https://manager:8443` with the installation CA; certificate verification stays
enabled. This loopback-only profile uses `http://localhost:5173` and explicitly
allows development cookies. Open `http://localhost:5173/app/`, sign in, choose
an organization and open **HAProxy**. Enrollment and cluster creation require
the account's normal authenticator MFA. Local sign-in messages are available
at `http://localhost:4000/dev/mailbox` when the local mail adapter is selected.

The standalone demo's cluster belongs to its installation operator organization.
Converge organizations have their own isolated fleet. Create a cluster and
choose **Enroll node**. The guide asks for a node name and HAProxy metrics
address, then generates one complete Docker install command after MFA. Copy it
into a terminal on the machine indicated in the dialog. The local profile uses
the existing demo HAProxy and Docker network, so it runs on this development
machine. Docker must already be installed. The command creates private
credential/CA files and a persistent identity directory under
`${XDG_DATA_HOME:-$HOME/.local/share}/converge-haproxy/<setup-id>` and starts a
restricted, restartable Docker agent. It does not modify HAProxy configuration.

Keep the dialog open: it tracks the exact node that consumes this command's
token, then separately confirms a fresh successful scrape containing HAProxy
measurements. Another node with the same name cannot satisfy this check.
Expired/revoked credentials, unavailable queries and failed scrapes do not show
success. Choose **View node** after confirmation. Closing the dialog stops its
requests and clears the command from console memory; it does not stop the
agent. Treat the copied command as a private credential and run it once.
Subsequent restarts use the saved identity, not a new token.

For independently deployed managers, configure `HAPROXY_AGENT_IMAGE` with an
image you publish containing `/usr/local/bin/haproxy-agent`, preferably pinned
by digest. No public agent image is published by this increment. Set
`HAPROXY_AGENT_MANAGER_URL` and `HAPROXY_AGENT_INGEST_URL` to node-reachable HTTPS
origins. `HAPROXY_AGENT_NETWORK` defaults to `host` for Linux Docker and
`HAPROXY_AGENT_METRICS_URL` defaults to `http://127.0.0.1:8405/metrics`.
The installer includes only the public installation CA, never its signing key.
Without these settings the guide explains that installation settings must be
configured; ordinary enrollment and the standalone operator API still work.
These settings belong to Go and work independently of Converge.

For manual agent startup use the command below. Manager and ingestion
addresses must be reachable from that node; the demo's loopback ports support
agents on this machine only. A token is displayed once and expires after ten
minutes. Revoking a node stops management/telemetry access while HAProxy keeps
serving its applied configuration.

The optional overlays are `compose.haproxy.yaml` at the repository root and
`compose.console.yaml` in this directory. Normal root Compose commands omit
the former and disable the handoff on recreation; `make demo` omits the latter
and recreates the manager with browser login disabled. Both retain database
data. Repeat `make haproxy-console` to restore the integrated profile.
The overlays assume the default `http://localhost:5173` console origin. If
existing host Vite/Phoenix processes already use those ports, select separate
ports and matching origins instead of stopping them:

```sh
CONSOLE_PORT=5174 API_PORT=4001 METRICS_PORT=9465 \
  PUBLIC_ORIGIN=http://localhost:5174 \
  HAPROXY_BROWSER_ORIGIN=http://localhost:5174 make haproxy-console
```

The Go browser origin must match the console's public origin exactly. Browsers
can resolve `localhost` to an existing IPv6 host process even when Docker also
publishes the same port over IPv4. Stop the root Converge stack with
`make down` before running `make -C apps/haproxy_manager down`, so the console
detaches from the HAProxy network before Compose removes it. Neither command
removes the stored data unless volumes are explicitly deleted.

For a hosted installation:

1. Provision a dedicated random secret: base64 encoding of 32–128 bytes. Set
   Phoenix's `HAPROXY_LOGIN_SECRET_FILE` and Go's
   `--browser-login-secret-file` (or `HAPROXY_LOGIN_SECRET_FILE`) to private
   regular files containing the same secret. Alternatively each accepts
   `HAPROXY_LOGIN_SECRET`; never configure both sources in one process. The key
   is separate from Phoenix cookies and operator/node credentials.
2. Set Go's `HAPROXY_BROWSER_ORIGIN` (or `--browser-origin`) to the exact HTTPS
   console origin. Keep secure cookies enabled and the default `/api/haproxy`
   cookie path. `HAPROXY_LOGIN_AUDIENCE` must match in both processes and defaults
   to `haproxy-manager`. Mount this secret in the manager only, never ingestion
   or agents. Shared-key rotation requires a coordinated restart of both
   issuers/verifiers. Existing opaque sessions expire within their 60-second
   lease; outstanding challenges/grants and old CSRF tokens stop verifying.
3. Set the console Worker's `HAPROXY_API_ORIGIN` to the independent HTTPS manager
   origin with a publicly trusted certificate. The Worker streams the browser
   API namespace directly to Go, strips Phoenix/operator credentials and passes
   only manager cookies. It excludes enrollment, renewal and metrics-write
   machine APIs. With no origin configured the namespace returns 503.

Only challenge-bound login grants go through Phoenix. Read grants require
`haproxy.read`; explicit management grants also require `haproxy.manage` and
recent strong MFA. Grants expire in 30 seconds and are consumed once. Go stores
its own opaque, HTTP-only session for at most 60 seconds after authorization;
the active console refreshes every 30 seconds. SSE checks expiry and revocation
before publishing. Converge role/session changes take effect within that lease.
The console attempts immediate manager logout on sign-out, including after a
page refresh. Mutations validate the manager's CSRF token and exact Origin.
Neither grants nor CSRF tokens are persisted in browser storage.

The fleet endpoint returns at most 100 nodes per cursor page. The console shows
ten rows per page and can load subsequent server pages. Storage failure keeps
the inventory visible with an explicit unavailable state. Freshness, scrape
availability and node revocation are separate; missing telemetry never means
zero traffic or proves that HAProxy stopped serving. Selected-node updates
arrive every five seconds. Bandwidth and response rates need two fresh samples;
counter resets, collection gaps and changed labels suppress invalid rates.
HAProxy response latency is shown as the exported average in milliseconds.

For host Vite development, `VITE_HAPROXY_PROXY_TARGET` defaults to
`https://localhost:18443`. `HAPROXY_DEV_CA` can select another trusted CA file;
the default uses the local demo CA if present. Restart Vite after first creating
that CA so its HTTPS agent loads the trust file. No service credentials belong
in Vite variables. The Go backend and standalone operator API require none of
these Converge integration settings.

### Connect DNS in Converge

Open an application's detail drawer, choose **Connect DNS**, select an accessible
zone containing its hostname, and enter the IP or hostname of your traffic
entrypoint. **Prepare DNS draft** requires the current successful application
version to be loaded and passing fresh entrypoint checks on every enrolled node.
You need both HAProxy management and DNS zone/name management permissions, with
the existing Converge management login/MFA.

The manager emits a signed 60-second intent through
`POST /v1/clusters/{cluster}/applications/{application}/dns-intent`. Phoenix consumes
it through the normal shared-login API and saves an idempotent app/version link
with an ordinary working DNS version. Review, approval, provider apply and public
DNS verification remain in the zone. Existing records and unpublished drafts are
preserved; conflicting records and stale versions fail without changing either
service. No provider write or HAProxy mutation is triggered by preparing DNS.

The entered DNS address is operator-supplied and is not verified by the existing
customer-side probes. Standalone manager/agent deployments keep working without
this optional Converge integration. See [ADR 0039](adr/0039-haproxy-connect-dns.md).

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
