# ADR 0046 — Node-local request path collection

- Status: Accepted
- Date: 2026-10-26

## Decision

Extend ADR 0045 with optional request paths. The same Go manager/agent and private
VictoriaLogs workload serve cloud and standalone installations. HAProxy changes
still use permissions and local authority, without approvals; DNS is unchanged.

The operator installs a `cv2` frontend log profile and a local agent file using
`--request-path-policy`. Without that file, path values are withheld. Recommended
`sanitized` mode matches ordered configured route patterns and exports only the
pattern, for example `/orders/:id`; unmatched paths are withheld. `:name` matches
one nonempty segment and terminal `*name` matches a nonempty suffix. Static paths
are explicit patterns too. No heuristic guesses which literal segments are safe.

An explicit local `exact` mode records other valid paths while replacing matches
from its ordered `redact` patterns. The manager/console cannot enable that mode
or edit these local rules. It can expose the reported mode and policy fingerprint.
Operators must treat exact paths and literal pattern segments as potentially
sensitive and configure redaction/retention for their applications.

HAProxy captures `path` into a transaction variable during request processing and
logs its length and hex bytes only to the private local socket. The agent strips
query/fragment text defensively, percent-decodes once, rejects ambiguous encoded
slashes, double encoding, dot segments, control/non-ASCII bytes and values over
512 bytes, then applies the local policy **before** queueing or transport. Raw
path bytes never enter the Go request batch, error logs or cloud storage. The
frontend profile must not be forwarded to another logging destination. The demo
sets an explicit socket-only frontend target rather than inheriting stdout.
Local capture follows the managed TLS guard prefix; changing that ordering is
rejected by discovery. Legacy absent path kinds are normalized before aggregation
so counts and percentiles are computed over the same unavailable-path group.

`cv1` remains supported. Legacy, disabled, unmatched and invalid path states are
distinguished; no path is fabricated or backfilled. Paths remain in request
storage and never become VictoriaMetrics labels or log stream identity fields.
The existing app/configuration identity and tenant checks remain authoritative.

## Queries and console

Existing tenant-bound `traffic` and `requests` endpoints add an exact collected
`path` filter. `path_order=requests|errors|latency` ranks at most ten path groups
by count, HTTP 5xx count or estimated p95. A bounded extra query runs after the
three core traffic queries to preserve private-store concurrency headroom.
Group identities and values are validated before returning them. Existing scope,
query deadlines, storage budgets, revocation checks and cursor boundaries remain.

The request drawer adds path filtering, a top-paths table, per-node collection
mode, request-row paths and request details. Selecting a path filters the existing
charts and request list. Unknown/withheld paths stay visible as labeled groups.
Percentiles describe recorded HAProxy total durations and retain their existing
sampling/loss limitations. Exact mode can produce many path groups; bounded
queries and local retention do not establish production capacity.

## Evidence

Checked 2026-10-26 against the pinned HAProxy 3.4.7 / DPA 3.4.5 demo and private
VictoriaLogs v1.53.0. Sources: [HAProxy transaction variables, path fetch and converters](https://docs.haproxy.org/3.4/configuration.html),
[DPA specification](https://raw.githubusercontent.com/haproxytech/dataplaneapi/v3.4.5/specification/build/dataplane_spec.yaml),
and [LogsQL grouping, sorting and quantiles](https://docs.victoriametrics.com/victorialogs/logsql/).
`make haproxy-paths` withdraws/drains each node for the local profile upgrade,
tests real HTTP/HTTPS path grouping, latency/failures, filters and pagination,
checks private dynamic/query/unmatched values are absent, and monitors app B
throughout. Parser/policy and tenant API tests cover exact-mode redaction,
encoded input, malformed/truncated logs and legacy records.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
