# ADR 0038: Application details and customer-side entrypoint probes

Status: Accepted

Date: 2026-10-25

Expose application delivery separately from node configuration success. The Go
manager supplies application-level status and the console offers a detail drawer
for routes, loaded versions, backend health, TLS, request checks and deployments.
HAProxy edits retain direct permissions, local policy and existing MFA, with no
review or approval stage.

## Authority and execution

An optional agent `--probe-policy-file` defines up to 32 explicit GET checks.
Each grants a named destination, port, protocol, frontend, exact application
hostname/path prefix, request path, expected HTTP status and optional response
header/value assertion. Requests are emitted only for matching locally observed
managed applications; a more specific neighboring route cannot satisfy the
grant. The manager and browser cannot select destinations, paths or trust files.

Policy destinations resolve once at agent control-session startup and requests
dial a pinned IP. Private addresses are intentionally supported; unspecified,
multicast and link-local addresses are rejected. Environment proxies and
redirect following are disabled. DNS/address changes require a fresh agent
session. Policies, trust roots and response expectations remain node-local.

HTTPS uses normal chain, expiry and hostname verification, TLS 1.2 or newer,
system trust plus an optional bounded local CA file. This is distinct from the
loaded-leaf pin check in ADR 0037. No verification bypass is available.

Checks run in a separate worker every five seconds, with at most four concurrent
requests, a 1.5-second request timeout, a four-second total batch budget, 8 KiB
response headers and 64 KiB response bodies. Nothing is written to HAProxy.
Only bounded result codes, timing, observed app digest, certificate leaf hash
and expiry cross the outbound mTLS control stream. Raw errors, response content,
header expectations and request credentials are never reported or logged.

## Observation and UI semantics

The existing node-control JSON storage persists the latest observations in Go's
dedicated PostgreSQL database; memory storage uses the same projection. No new
Phoenix storage, telemetry proxy or approval contract is introduced.
`GET /v1/clusters/{cluster}/applications/status` requires `haproxy.read`, resolves
the tenant, and combines immutable application revisions with up to 12 active
nodes' observations. The browser proxy exposes this read endpoint only.

Configuration matches require fresh typed configuration and matching runtime
backend identities/initial weights. Backend health remains separate. A deployed
revision does not imply request reachability. Checks refer to an exact canonical
application digest; old results cannot verify a changed application. A fresh
failed request yields unhealthy, while absent, disconnected, expired (15 seconds)
or mismatched observations yield unknown. The UI also expires cached status
after a failed refresh; it retains application history and context.

Reachability is evidence from the listed customer-side locations, not proof of
public access, every upstream node, or an exact application binary revision.
Optional response assertions improve route checking without adding public
headers or claiming cryptographic application identity. Public probe services,
configuration of probe grants from the console, alert delivery, long-term probe
history and using end-to-end probes as a rollout gate remain separate increments.

## Verification

Contract tests cover tenant/read permissions, protocol round trips, PostgreSQL
and memory persistence, freshness and revision correlation, hostname/chain
validation, redirects/proxies, bounded responses and neighboring route isolation.
The real two-node demo exercises both apps over HTTP/HTTPS, one failed backend
with its neighbor healthy, lost agent evidence and automatic recovery.

API evidence checked on 2026-10-25: [Go HTTP Transport](https://pkg.go.dev/net/http#Transport),
[Client redirect policy](https://pkg.go.dev/net/http#Client), and
[TLS configuration](https://pkg.go.dev/crypto/tls#Config). The repository's pinned
Go toolchain is used for compilation and tests; no new provider API is added.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
