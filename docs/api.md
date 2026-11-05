# Manager and agent configuration

Configure the Go services and use their tenant-scoped operator API.

## Service and agent configuration

The manager binary runs the API or ingestion role. Both use the dedicated
PostgreSQL database; only numerical observations go to VictoriaMetrics.

```sh
haproxy-manager init --state-dir /private/new-installation \
  --dns-name manager.example.com --dns-name ingest.example.com

export HAPROXY_DATABASE_URL='postgres://USER:PASSWORD@HOST/DATABASE?sslmode=verify-full'
export HAPROXY_ORGANIZATION_ID='UUID-FROM-organization-id'
haproxy-manager serve --role manager --migrate --listen :8443 \
  --pki-dir /private/new-installation/pki \
  --tls-cert /private/new-installation/pki/server.crt \
  --tls-key /private/new-installation/pki/server.key \
  --admin-token-file /private/new-installation/admin-token \
  --metrics-url http://private-victoriametrics:8428
```

Run a second process with `--role ingest`, its own listener, no operator
credential and a PKI directory containing only `ca.crt`, `server.crt` and
`server.key`. Both roles use the same database schema and installation authority.
Keep database and VictoriaMetrics access private; the `sslmode=disable` database
setting in Compose is confined to this isolated local example.

`--migrate` explicitly installs the manager schema. Normal service starts can
omit it after a separate migration step; the local manager enables it for the
disposable development database. Migration requires its own database authority
in a deployed installation.

The installation operator credential is scoped to its configured organization.
Operator requests also supply `X-Organization-ID`; a different organization is
rejected. For a multi-organization deployment,
`--operator-credentials-file` loads an array of objects containing
`organization_id` and `token_sha256`, with the same tenant-bound behavior.
These are server-side operator credentials, not customer browser tokens.
Operator credentials are loaded at process startup; rotating or revoking one
requires a configuration rollout. Node and enrollment-token revocation use the
control database and take effect on subsequent authenticated requests.

Enrollment tokens are expiring and single-use. The agent creates its private
key locally, submits a CSR and receives a certificate bound to its organization,
cluster and node. Normal exports use mutual TLS; request labels cannot select
a different tenant. Node revocation is checked against manager state. The agent
needs outbound access to the manager and ingestion endpoints, and local access
to a supported HAProxy Prometheus listener.

```sh
haproxy-agent --server-url https://manager.example.com \
  --ingest-url https://ingest.example.com \
  --server-ca /etc/haproxy-agent/ca.crt \
  --enrollment-token-file /etc/haproxy-agent/enrollment-token \
  --state-dir /var/lib/haproxy-agent --name haproxy-1 \
  --metrics-url http://127.0.0.1:8404/metrics --interval 1s
```

Create an unprivileged `haproxy-agent` account and a private state directory for
the [systemd unit](../deploy/systemd/haproxy-agent.service). Replace the example
hostnames and provide readable CA/token files with restricted permissions before
enabling it. The agent requires neither root nor HAProxy configuration write
access for observation-only enrollment. Keep its metrics listener on loopback or a private
container network.

## Declared topology API

`PUT /v1/clusters/{cluster}/topology` requires tenant-scoped `haproxy.manage`
authority (and Origin/CSRF for a browser session). It stores and audits metadata:

```json
{
  "entrypoint": "http://localhost:18081/",
  "strategy": "roundrobin",
  "frontend": "public",
  "backend": "app-a:8080, app-b:8080",
  "nodes": [{ "node_id": "<enrolled-node-uuid>", "address": "haproxy-a:8080" }]
}
```

`GET /v1/clusters` and `/v1/fleet` include this topology. Nodes must belong to the
same organization and cluster; revoked nodes cannot be newly attached. The
manager never contacts a declared address or changes HAProxy configuration.
Entrypoint reachability is not inferred from agent health. Declared nodes'
traffic queries select the named frontend, excluding statistics/health-check
traffic when those use a separate frontend. Changing this metadata changes the
query scope, including historical queries; it does not rewrite stored samples.
Cluster comparison charts are bounded to the first 12 loaded active nodes, with
an explicit limit notice. Larger fleets retain paginated inventory and individual
node history. Totals are labeled as observed when samples are missing.

## Operator API

All operator endpoints require a bearer credential and matching
`X-Organization-ID`. Bootstrap and smoke operations in `scripts/demo.py` show
the HTTPS requests without placing secrets in command-line arguments.

| Method and path                                | Request or result                                                      |
| ---------------------------------------------- | ---------------------------------------------------------------------- |
| `POST /v1/clusters`                            | `{"name":"Production"}` creates a cluster                              |
| `GET /v1/clusters`                             | Organization-scoped cluster inventory                                  |
| `GET /v1/fleet?cursor=UUID`                    | Bounded node inventory, clusters and freshness                         |
| `GET /v1/agent-setup`                          | Installation settings and public CA for the guided setup               |
| `POST /v1/enrollment-status`                   | `{"token":"..."}` tracks its enrolled node within the organization     |
| `POST /v1/clusters/{id}/enrollment-tokens`     | `{"ttl_seconds":600}` creates a one-use token                          |
| `GET /v1/nodes`                                | Organization-scoped inventory                                          |
| `GET /v1/nodes/{id}/metrics`                   | Bounded current metrics for an authorized node                         |
| `GET /v1/nodes/{id}/history?range=1h`          | Stored bandwidth, response, connection and latency time series         |
| `GET /v1/nodes/{id}/events`                    | SSE updates with bounded polling and freshness                         |
| `GET /v1/clusters/{id}/events?nodes=UUID,UUID` | One-second live summaries for up to 12 authorized cluster nodes        |
| `GET /v1/nodes/{id}/discovery`                 | Runtime inventory, control freshness and latest 50 operations          |
| `POST /v1/nodes/{id}/operations`               | Direct drain/restore/weight with generation/state/weight preconditions |
| `GET /v1/clusters/{cluster}/applications/{app}/versions/{revision}/traffic-rules?range=15m` | Exact-spec recorded rule counts and collection coverage (`haproxy.read`) |
| `POST /v1/nodes/{id}/revoke`                   | Revokes node access                                                    |
| `DELETE /v1/enrollment-tokens`                 | `{"token":"..."}` revokes an unused bootstrap token                    |

Application specs may include `traffic_policy` version 1, with `mode`
(`observe`/`enforce`), `unknown_action` (`allow`/`block`) and an ordered `rules`
array. Rules have UUID `id`, `name`, `action`, optional `path_prefix` and typed
`cidrs`, `countries`, `asns` arrays. See [traffic rules](traffic-rules.md) for
host grants, bounds and deployment semantics. Writes use the existing
permissioned application deployment API. Request search also accepts
`traffic_rule` (a rule UUID or `unknown`) and `traffic_decision` (`allow`, `block`,
`would_allow`, `would_block`); these filters never accept HAProxy expressions.

`POST /v1/enroll` accepts a bootstrap token and CSR over server-authenticated
HTTPS. `POST /v1/identity/renew` requires the current node certificate.
`POST /v1/metrics/write` is exposed by ingestion only and accepts bounded
Prometheus Remote Write batches authenticated with the node certificate.

Metrics describe the installed HAProxy observation point. Missing samples are
gaps, not zero traffic or proof that HAProxy stopped serving. The initial
collector exports a bounded supported metric set; it does not promise exact
request search, HTTP visibility through TLS passthrough, or p95/p99 derived from
HAProxy timing averages. Export failures do not block the serving process.

The initial wire contract limits batches to 256 KiB compressed, 2 MiB decoded,
1,000 series and one sample per series. Scrapes are bounded to 2 MiB and use a
supported metric allowlist. Undefined exporter values are omitted. Ingestion
also limits concurrent uploads, one upload per node, and active series over a
rolling five-minute window: 1,000 per node, 10,000 per organization, and 100,000
per ingestion process. These admission budgets are local to a process; shared
quotas across replicated ingestion services need a later coordination layer.

The agent has one bounded scrape/export operation at a time, a five-second
operation deadline, and at most one transient retry with jitter. It reports
scrape and export failures and persists its identity and a bounded runtime-operation journal. Live metrics snapshots
include collection time, freshness and scrape availability separately. The SSE
endpoint emits a snapshot immediately and refreshes every five seconds.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
