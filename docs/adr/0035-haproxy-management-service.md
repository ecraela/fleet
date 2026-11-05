# ADR 0035: HAProxy management service

Status: Accepted

Date: 2026-10-23

Accepted: 2026-10-24

Revised: 2026-10-25. Customer-owned traffic infrastructure replaces the original
Ecraela provisioning scope; hosted and self-hosted refer to management deployment.

## Context

HAProxy cluster management needs configuration deployment, operational actions
and frequent traffic observations. Customers provision and own their HAProxy
nodes, application servers and traffic networking, typically in their cloud
provider. Management supports hosted and independently self-hosted deployments.

Converge already owns DNS, accounts, organizations, permissions, and provider
orchestration. Its zone-owned traffic policies in ADR 0033 describe provider
load balancers. HAProxy clusters can span zones and serve TCP traffic without a
DNS zone, so they need a separate resource model. Existing inbound enterprise
SAML/SCIM in ADR 0025 does not provide identity federation to a sibling product.

The management system must protect serving traffic during collection overload,
storage failure, controller outages, and interrupted deployments. Telemetry loss
is acceptable within a visible policy. Desired configuration and operation
outcomes require durable storage and reconciliation.

## Decision

Build an independent Go service and Go node agent. Keep the Converge product
experience through shared UI components, organization context, account linking,
and optional DNS bindings. Converge's Phoenix service and database remain outside
the HAProxy ingestion, telemetry query, and node-command paths.

Use one Go codebase with two independently deployed server roles initially:

| Component            | Responsibility                                                                                                                                                |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Manager              | Product API, local authorization, cluster inventory, agent enrollment and control, configuration revisions, deployment jobs, audit, bounded telemetry queries |
| Ingestion            | Authenticate telemetry, enforce tenant and node limits, decode bounded batches, write metrics, expose ingestion health                                        |
| Agent                | Observe a local HAProxy instance, execute permitted local operations, report running configuration, aggregate and export observations                         |
| Dedicated PostgreSQL | Manager state, durable jobs, configuration history, agent identities, local authorization projection, audit                                                   |
| VictoriaMetrics      | Numerical traffic and host observations                                                                                                                       |

Deploy manager and ingestion with independent CPU, memory, connection, and queue
limits. Separate database and storage credentials from Converge. Use independent
database capacity for the hosted product so resource isolation extends beyond
connection pools. Bound query concurrency, time ranges, returned points, and
execution time; split query serving into another process only when measurements
justify it. A broker is unnecessary for the initial best-effort telemetry path.

Go is the initial choice because HAProxy provides a maintained Go configuration
and Runtime API library and a Data Plane API implemented in Go. This reduces
integration work; it is not a claim that Go will outperform Rust or Elixir for
every workload. Resource and traffic benchmarks determine the supported limits.

## Resource and ownership model

Model installation, cluster, proxy node, listener or frontend, backend pool,
backend server, configuration revision, deployment, and runtime operation.
Record desired and observed generations separately for each proxy node.

An optional DNS binding references a Converge organization,
zone, hostname, and the cluster's traffic endpoint. It neither owns the cluster
nor requires every cluster to have a domain.

Distinguish removing an application server from a backend pool, removing an
HAProxy node from management, and destroying a machine. The manager has no
machine provisioning or destruction authority.

## Node connectivity and local access

Install an agent beside each HAProxy node: a systemd service on Linux machines
or a sibling container with explicitly shared interfaces. Start with Linux
systemd and Compose deployment profiles. Kubernetes ingress-controller support
requires integration with its configuration owner rather than edits to generated
HAProxy files.

The agent initiates two separate outbound connections authenticated with mutual
TLS:

1. A bidirectional gRPC control stream with Protobuf messages for heartbeats,
   desired state, scoped operations, and results.
2. A telemetry connection to ingestion, using Prometheus Remote Write for the
   initial metrics protocol. This already uses Protobuf. Future sampled events
   can use separately bounded Protobuf batch RPCs.

Keep control and bulk telemetry on separate connections and resource budgets.
No public Runtime API, Data Plane API, incoming customer firewall rule, general
network tunnel, or remote shell is needed. A future site relay can serve
networks where individual nodes cannot reach the management endpoint.

Enrollment uses an expiring, single-use bootstrap credential to establish a
machine identity bound to the installation, organization, and node. Normal
connections use renewable credentials with revocation. Agents cannot choose
their tenant through request fields. Human login sessions and Converge's
read-only organization API keys are not agent credentials.

Use local Unix sockets for Runtime API access and local Data Plane API access
for durable configuration. Keep the Data Plane API as the sole configuration
writer for the initial managed profile. The agent must not also rewrite its
files through a second configuration path. Credentials and TLS private keys
stay local where possible; configuration views and audit omit secrets.

The network-facing agent gets bounded local capabilities. A narrowly privileged
helper can perform authorized reload operations where required. Avoid unrestricted
root execution, host filesystem access, or Docker socket access.

## Configuration and operational correctness

Discover configuration as soon as a node connects. Authorized operators apply
HAProxy changes directly; HAProxy does not use Converge’s DNS plan, review, or
approval workflow. There is no per-generation adoption step. The local agent’s
runtime-control policy determines whether a node permits mutations. Observe
external configuration changes and reject stale operation preconditions.
GitOps, an ingress controller, or an external administrator can retain ownership
of durable configuration while permitting temporary runtime changes.

Keep append-only operation history. Durable backend configuration changes retain
versions and support rollback as a new forward change. Versioning and rollback
do not introduce a review or approval requirement.

Use the Runtime API for supported live operations such as state, weight, and
map or ACL changes. These changes affect memory and ordinarily disappear on
reload. Each operation therefore declares whether it is temporary, an expiring
override, or durable desired state. Changing algorithms, rule structure, or
unsupported dynamic resources can require a validated configuration reload.

Represent deployment as prepare, validate, apply, observe, and verify per node.
Validation uses the installed HAProxy version, configuration dependencies,
certificates, and execution environment. Preserve a last-known-good artifact.
An accepted Data Plane API transaction or queued reload is not verified serving
behavior. Observe the resulting process/configuration generation and run health
checks after application.

Cluster deployments are staged workflows with partial results, not atomic
transactions. For an active/standby pair, validate both and update the standby
first, verify it, then perform a controlled handover before updating the former
active node. Other topologies need an explicit availability budget. Rollback is
a new forward deployment referencing an earlier artifact.

Persist operation IDs, expected base generations, target identities, deadlines,
and outcomes independently of telemetry. Serialize node mutations and fence old
controller sessions after reconnect or failover. A timeout has an unknown result
until reconciliation; do not blindly replay non-idempotent side effects.

Drain has explicit completion criteria and a deadline. HAProxy drain removes
normal balancing selection but can still admit persistence-routed connections.
Maintenance and force-closing sessions are distinct actions. Draining a whole
proxy node also requires withdrawing its traffic endpoint or upstream selection.

## Customer-owned traffic infrastructure

Customers create machines, HAProxy processes, application servers and networking
using their provider or infrastructure tools. Install and enroll an agent beside
each HAProxy node, then discover and manage it according to permissions and local
policy. The manager does not host application workloads or sell traffic capacity.
The Compose pair is a local acceptance lab, not a hosted infrastructure product.

Separate the management domain from service domains. A management hostname
points to Ecraela's service edge. A customer's application hostname points to a
traffic VIP, provider load balancer, or other explicit service endpoint.

A shared VIP requires a separate failover implementation: Keepalived/VRRP where
the network supports it, a provider floating IP, or an upstream load balancer.
DNS alone does not create HA between the two HAProxy nodes. Customers supply
and test their chosen traffic endpoint. Converge can manage an explicitly enrolled
HAProxy entrypoint; provider-specific withdrawal adapters remain separate work.

Converge publishes or changes A/AAAA records only through its existing reviewed
DNS workflow after the traffic endpoint and TLS are ready. HAProxy deployment
and DNS publication are separate recoverable steps; failure does not imply
automatic DNS publication.

## Shared console and cloud identity

Use the Converge shell, design system, product navigation and organization
selection. The first console integration routes `/api/haproxy` directly to Go
at the edge under the console origin. A dedicated product hostname remains a
deployment option. Phoenix never proxies the management or telemetry requests.

The initial identity handoff uses a dedicated 32–128-byte shared signing key.
Go issues a browser-bound challenge cookie; Phoenix checks current authority
and signs a nonce-bound grant valid for 30 seconds. Go atomically consumes its
unique ID and creates an opaque session expiring no later than 60 seconds after
the authorization check. Active views refresh every 30 seconds. Claims include
immutable user/organization IDs, issuer, audience, issued/expiry times,
capabilities and global versus organization-restricted session kind. Management
requires recent strong MFA at grant issuance. Go checks expiry on each request
and before each SSE publication. The maximum stale authorization window is the
60-second lease, including logout and role or membership revocation.

The manager owns its HTTP-only, host-only SameSite=Strict cookie and CSRF
token; browser mutations require the configured exact Origin. Grants and CSRF
tokens stay in browser memory. The proxy passes only manager cookies and
browser API paths, keeping Phoenix cookies and installation operator keys out
of Go. Never copy Converge's session encryption keys or use its database as an
integration API. A shared OIDC identity provider is the longer-term option.

Preserve immutable user and organization identifiers, authentication assurance
and age, audience, and organization-restricted SSO context. Resolve HAProxy
capabilities and cluster scope in the manager. DNS access alone must not grant
production cluster access. Membership and entitlement changes need bounded
propagation; privileged operations require fresh authority and fail closed when
that authority cannot be established. Existing traffic keeps its applied state.

Converge stores only integration metadata and optional compact audit summaries.
The manager owns detailed operational audit. Metric samples, request events,
agent heartbeats, and dashboard queries do not flow through Phoenix.

## Independently self-hosted deployment

Ship the same manager, ingestion role, UI, agent protocol, and storage schema
for self-hosting. The small profile runs manager, ingestion, PostgreSQL, and
single-node VictoriaMetrics with an installation-owned identity authority and
local recovery account. Local enrollment, authentication, authorization,
upgrades, and normal operation work without Ecraela Cloud. Customer OIDC can be
configured independently. Cloud and self-hosted are deployment modes; Community
and Enterprise are separate packaging and support decisions.

Each node has one authoritative manager. Connecting a self-hosted installation
to Converge later is an optional outbound connector with explicit delegated
capabilities and revocation. Commands pass through the local manager and local
policy; cloud and local controllers never independently write the same nodes.
The standalone product ships before this optional federation mode.

## Telemetry policy and storage

Collect bounded HAProxy and host counters locally. As an initial benchmark
profile, test five-second metric collection and prompt state-change reporting.
Selected active views can request one-to-two-second collection and updates
within an explicit node budget. These are targets to validate, not measured
guarantees.

Aggregate request timing histograms locally where logs or supported
instrumentation provide them. Standard HAProxy timing averages cannot produce
valid p95/p99 values. Aggregate compatible histograms across nodes rather than
averaging node percentiles. Keep exact HTTP codes and request details dependent
on available request instrumentation. TCP passthrough and end-to-end encrypted
traffic do not expose HTTP codes without an appropriate observation point.

Limit metric dimensions to configured resources and bounded status categories.
Arbitrary URLs, client addresses, and request IDs do not become metric labels.
Add ClickHouse when retained sampled request search is implemented. Add optional
object storage for preserved diagnostic windows. Keep eBPF diagnostics as a
later helper with separate permissions, kernel compatibility, memory accounting,
and duration limits; it is not required for baseline management.

Bound queue bytes, decoded record sizes, active series, parser state, in-flight
requests, observation age, and export bandwidth. Retry only within those limits,
with backoff and jitter. Drop expired observations and reduce collection under
pressure. Disable telemetry disk spooling by default. Ensure the actual syslog,
file, or container logging path also has bounded nonblocking behavior and
rotation so pressure cannot move backward into HAProxy.

Ingestion applies per-tenant and per-node admission limits, bounded writer
concurrency, and storage batching. Reject overload promptly. A successful
request means the configured ingestion acknowledgment contract was met; it
does not promise per-batch fsync or replication. If a receiver accepts only
into RAM, it must expose that crash-loss window. Track accepted, stored,
rejected, expired, and failed observations separately. No exactly-once claim
is made for telemetry.

Self-hosted single-node VictoriaMetrics stays behind the manager and ingestion
APIs. Every installation enforces organization and cluster permissions before
constructing storage queries. Hosted storage also enforces tenant routing on
every write and query; an agent-supplied tenant label is not authorization.
Select VictoriaMetrics cluster tenant routing or isolated tenant stores
according to measured scale. Do not pass caller-controlled tenant headers or
unrestricted query filters through to storage. Never expose database query or
administrative endpoints directly to browsers or customer agents.

Show management connectivity, HAProxy serving health, VIP ownership, desired
and running revisions, telemetry freshness, and collection gaps separately.
Missing observations are not zero traffic or proof of a dead proxy. Existing
HAProxy traffic and local failover continue through cloud outages, agent
restarts, and license-service failures.

## Implementation sequence and acceptance

Build customer-owned node management on shared enrollment foundations in this order:

1. Define supported HAProxy/Data Plane API versions, the Linux blueprint, node
   identity, control protocol, configuration ownership, and resource budgets.
2. Enroll an existing node, display inventory, bounded metrics, configuration
   drift, freshness, and collection cost in cloud and standalone installations.
3. Exercise a real two-node customer-side lab through the same enrollment path.
   Verify its explicit traffic endpoint and failover mechanism.
4. Deliver audited drain/restore and weight operations, then validated staged
   configuration rollout, rule and algorithm changes, recovery, and safe node
   removal. Add reviewed DNS binding/publication after endpoint readiness.
5. Verify shared cloud account handoff, tenant and cluster permissions,
   revocation, self-hosted local login, and offline traffic continuity before
   calling either deployment profile ready.

Acceptance scenarios include saturated telemetry, slow storage, expensive
queries, management disconnection, expired credentials, duplicate commands,
conflicting configuration writers, invalid configuration, failed reload,
partial cluster rollout, interrupted enrollment, and controller restart.
Measure HAProxy throughput and tail latency against a baseline alongside total
agent CPU, RSS, buffering, and export bandwidth. Select supported budgets from
those results. Demonstrate that telemetry overload does not consume the
resources reserved for serving or durable cluster operations.

## Sources and related decisions

Official references consulted on 2026-10-23:

- [HAProxy Native Go Client](https://github.com/haproxytech/client-native)
- [HAProxy Runtime API semantics](https://www.haproxy.com/documentation/haproxy-runtime-api/)
- [HAProxy Data Plane API](https://www.haproxy.com/documentation/haproxy-data-plane-api/)
- [Data Plane API transaction implementation](https://github.com/haproxytech/dataplaneapi/blob/master/handlers/dataplane/transactions/transactions.go)
- [HAProxy statistics reference](https://www.haproxy.com/documentation/haproxy-runtime-api/reference/show-stat/)
- [HAProxy management manual](https://cdn.haproxy.com/documentation/haproxy-configuration-manual/new/latest/management/)
- [Prometheus Remote Write](https://prometheus.io/docs/specs/prw/remote_write_spec/)
- [Prometheus histograms](https://prometheus.io/docs/practices/histograms/)
- [gRPC flow control](https://grpc.io/docs/guides/flow-control/)
- [VictoriaMetrics single-node operation and multitenancy limits](https://docs.victoriametrics.com/victoriametrics/)
- [VictoriaMetrics cluster tenancy](https://docs.victoriametrics.com/victoriametrics/cluster-victoriametrics/)
- [Grafana private connectivity](https://grafana.com/docs/grafana-cloud/observe-and-act/connect-externally-hosted/private-data-source-connect/)

Related decisions in the separate Converge repository are ADR 0021
(organization API keys), ADR 0025 (enterprise identity), and ADR 0033
(zone-owned provider load balancers). They describe that product's boundaries
and are not required to build or run HAProxy Manager.

## Implemented persistent editing profile (2026-10-25)

The next implemented slice adds node-local DPA 3.4.5 as the sole writer beside
HAProxy 3.4.7. It supports existing backend balance strategies and server
addresses, ports, weights, additions/removals and maintenance. A save creates
an immutable revision and directly queues a validated reload under
`haproxy.manage` and local agent policy. A restore creates a new forward version.
Both deployment modes use the same Go code; neither path introduces approval.

Full secret-bearing snapshots and commit receipts stay in bounded private agent
storage; only a safe projection and artifact references reach the manager.
Success requires the resulting full artifact, a new worker and loaded serving
settings. Interrupted writes reconcile without replay. The real two-node lab
persists separate node volumes and tests save, reload, restart and restore on
each node. Node editing is the transaction layer for the cluster rollout below.
Raw configuration, certificate and rule editing remain outside this profile.
See the [DPA evidence/profile](../providers/haproxy-dataplane.md).

## Implemented rolling update profile (2026-10-25)

The backend profile now supports durable cluster rollouts over the same Go
manager, agent and PostgreSQL in cloud or self-hosted mode. Starting a rollout
requires `haproxy.manage` and local configuration opt-in; there are no approvals.
The accepted input freezes all 2–12 active node identities and exact base
generations/artifacts/versions. Per-node snapshots remain private on each agent.
The coordinator serializes mutations, verifies completed HAProxy checks for a
five-second manager-timed window, and updates one node at a time.

Failure stops forward updates and restores changed nodes in reverse order, each
to its own baseline as a new version. Unsent work is cancellable; ambiguous
dispatched writes are reconciled after their deadline before compensation.
Manager restart resumes durable operation IDs. Membership or artifact drift
blocks unsafe continuation. Recovery that cannot complete is reported explicitly;
no component claims an unreachable node was restored. This profile does not
control a VIP or upstream routing, and does not promise atomic cluster changes.
See the [rollout runbook](../operations.md#rolling-cluster-updates).

## Implemented whole-node maintenance profile (2026-10-25)

The first maintenance adapter binds a same-tenant enrolled HAProxy entrypoint in
its own management cluster to explicit application-node/backend/server identities.
It uses the existing independent Go service, outbound agent channel, runtime
policy and DPA writer. `haproxy.manage` applies directly without approvals.
Runtime maintenance blocks new selection before persistent DPA withdrawal/reload.
A three-second zero-connection window on the declared target frontend verifies
drainage. The disabled upstream survives restart and stays reserved for explicit
rejoin. The target remains editable while held; its peers and upstream are fenced.
Rejoin checks target/peers for five seconds before enabling and target/upstream
for another five seconds afterward. Failed rejoin health starts withdrawal again.

Durable checkpoints and request/operation identities survive manager restart;
both clusters share sorted transaction locks and exclude overlapping rollouts or
maintenance. A deadline never kills sessions or releases a reservation. Missing
or stale connection counts never mean drained. Ambiguous outcomes and external
entrypoint changes remain fenced rather than replayed or overwritten. This
profile does not provide provider VIP/VRRP handover or control bypass traffic.
See the [maintenance runbook](../operations.md#whole-node-maintenance).

Official semantics checked on 2026-10-25:
[set server](https://www.haproxy.com/documentation/haproxy-runtime-api/reference/set-server/),
[disable server](https://www.haproxy.com/documentation/haproxy-runtime-api/reference/disable-server/),
[enable server](https://www.haproxy.com/documentation/haproxy-runtime-api/reference/enable-server/),
and [show stat](https://www.haproxy.com/documentation/haproxy-runtime-api/reference/show-stat/).
Runtime access remains node-local and permission-limited through the existing
agent; no new public HAProxy listener, credentials, pagination or provider rate
limits are introduced. The DPA 3.4.5/HAProxy 3.4.7 pins and failure/recovery
contract remain in the [provider evidence](../providers/haproxy-dataplane.md).

## Rolling updates with node maintenance (2026-10-25)

Managed-entrypoint rollouts now reuse the whole-node maintenance state machine.
The request freezes the entrypoint node, binding digest and exact generation,
artifact and DPA version alongside all target preconditions. The rollout reserves
both clusters in sorted UUID lock order. Each target is withdrawn in runtime,
withdrawal is persisted, and the traffic frontend must report zero connections
for three continuous seconds before its configuration changes. Five seconds of
healthy target/peer observations precede rejoin; another five seconds verify the
target and entrypoint before the next node starts. Local runtime and configuration
opt-ins remain required, as does `haproxy.manage`; no approvals are added.

Forward and reverse recovery retain the same durable operation identities.
Rollback restores each node's private starting snapshot and drains any node
that already rejoined before changing it again. The default drain deadline is
30 seconds (bounded to 5–300). A drain timeout or unverified recovery stops the
whole rollout and atomically hands the checkpoint to standalone maintenance.
This preserves isolation and an operator recovery path without withdrawing the
remaining serving peer. Unknown outcomes are not replayed and external artifacts
are not overwritten. A shared bound of 16 active maintenance/rollout coordinators
also reserves room for this handoff. Historical rollouts and existing unbound
standalone API integrations retain their original health-gated behavior; the
console requires a managed entrypoint for new rolling updates.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
