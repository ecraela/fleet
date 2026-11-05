# ADR 0045 — Private application request telemetry

- Status: Accepted
- Date: 2026-10-26

Path collection is extended by [ADR 0046](0046-haproxy-local-request-path-policy.md):
paths remain withheld by default, with node-local sanitized or explicit exact
collection. Other excluded request content and workload boundaries remain.

[ADR 0055](0055-haproxy-local-geography.md) adds node-local country/ASN estimates;
client addresses and forwarded headers remain excluded from the wire and storage.

## Decision

The independent Go stack adds opt-in application request collection. Its dedicated
PostgreSQL retains configuration/audit and VictoriaMetrics retains node metrics.
Completed requests and collection heartbeats use private VictoriaLogs v1.53.0.
Phoenix supplies optional login grants only. Cloud and standalone use the same API.

Application `request_telemetry` defaults off. Changing it creates an immutable
revision through rolling drain/update/verify/rejoin, with `haproxy.manage`, version
5 agents and local `--access-log-socket` opt-in. There are no HAProxy approvals or
DNS mutations. The operator installs the documented local Unix datagram target
and `cv1` frontend format; Converge never overwrites another log profile silently.

The compiler sets a safe intent fingerprint in `txn.cv_spec` in its owned backend.
Discovery verifies that exact rule. Backend identity, not client headers, selects
the app. The fingerprint identifies configuration content: repeated deployments
and rollbacks can match several versions, never a unique serving generation.

## Collection contract

Records contain app/fingerprint, backend server, classified method, status, bytes,
total/response/connect/queue timings, HTTP/HTTPS and termination state. The agent
timestamps receipt of completed transactions and assigns a process UUID/sequence
observation ID, not a client trace ID. Long requests appear on log emission.
Monitoring requests through the listener are included.

Raw URLs/paths, query strings, client addresses, all headers, cookies and bodies
are excluded. Errors never export raw datagrams; wire JSON rejects extra fields.
Ingestion authenticates the existing enrolled mTLS identity, checks revocation and
known app/fingerprint ownership, then stamps organization/cluster/node itself.

Separate reader, sender, connection and admission pools keep logs out of metric
and control workloads. Limits are 2 KiB datagrams, 2,048 queued records, 256
records/256 KiB batches, a 250 ms flush under load, five-second heartbeats and a
two-second delivery deadline. No disk spool or ambiguous-write retry exists.
Counters expose received/ignored/rejected/dropped/unconfirmed records for the
current process across its apps; process UUIDs distinguish resets. Pre-socket
loss is unmeasurable, and unconfirmed deliveries may have reached storage.
This is best-effort operational recording, not an exhaustive audit or traffic bill.

Ingest and queries each have a four-request admission pool and separate transport.
Node ingestion permits 100 batches per ten seconds with 1,024 live admission
identities maximum. Queries have bounded responses and deadlines. These are
resource bounds, not a measured capacity claim; log outages must not stop serving,
metrics or control.

## API and console

Two tenant-bound `haproxy.read` GET endpoints expose `traffic` and `requests`
under `/v1/clusters/{cluster}/applications/{application}`. Typed range, node,
protocol, status class, method, minimum time, fingerprint and observation-ID
filters construct server-owned LogsQL. No browser supplies query syntax, a
destination or ingestion identity. Browser authority is checked after querying.
The store has no published host port; deployment network isolation is required.

Counts, status classes, bytes, mean and estimated p50/p95/p99 derive from recorded
individual HAProxy total durations. Shared frontend counters are not duplicated
across apps, and rolling averages are not renamed percentiles. Histories have at
most 362 buckets for 15m/1h/6h/24h, with gaps for missing request buckets. Empty
recordings or listening sockets do not prove complete collection or zero traffic.
Search returns ten rows with a frozen time range and bounded time/ID cursor;
late arrivals may appear on refresh. Drawers contain filters, charts, collection
status and request details; application incidents link to the same investigation.
Refresh failures retain data with stale/error feedback; hidden polling pauses.

The local profile retains two days subject to a 1 GiB disk budget, which can evict
older daily partitions sooner. Production retention/privacy, backup and capacity
need explicit configuration and measured release gates. Content search, tracing,
fine resource scopes, sampling controls and durable node spooling remain open.

## Evidence

Checked 2026-10-26: [HAProxy 3.4 logging/timing](https://docs.haproxy.org/3.4/configuration.html#8.2.6),
the existing checksum-pinned [DPA 3.4.5 contract](https://raw.githubusercontent.com/haproxytech/dataplaneapi/v3.4.5/specification/build/dataplane_spec.yaml),
[VictoriaLogs v1.53.0](https://github.com/VictoriaMetrics/VictoriaLogs/releases/tag/v1.53.0),
[ingestion](https://docs.victoriametrics.com/victorialogs/data-ingestion/),
[queries](https://docs.victoriametrics.com/victorialogs/querying/) and
[LogsQL](https://docs.victoriametrics.com/victorialogs/logsql/).
The real DPA/HAProxy lab confirms `log_format` retains its single-argument quotes.

`make haproxy-requests` installs the demo profile one withdrawn node at a time,
enables both apps, checks real HTTP/HTTPS codes/latency, filtering, isolation,
pagination, history and log-store failure/recovery while app B serves. It compares
routing and shared TLS/listeners before/after. Unit/race and tenant API tests
cover transport, parsing, queues, local grants and foreign evidence.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
