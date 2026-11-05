# Configuration and node operations

Apply configuration and runtime changes through the standalone console or operator API.

## Persistent backend configuration

Select a node in the console, find **Persistent configuration**, and use the
backend menu's **Edit backend** action. Edit balancing strategy, server address,
port, weight, maintenance, or add/remove a server, then **Save and reload**.
`haproxy.manage`, a fresh control connection and local agent opt-in are required.
The existing shared-login MFA session still applies; there is no review or
approval. A version becomes current only after the new worker and loaded
settings are verified. **Version history → View settings / Restore version**
restores a previous full local snapshot as a new version. Each save addresses
one node by default. Select **Roll out to all nodes** for the health-checked
cluster update described below.

The pair automatically installs private Unix-only DPA 3.4.5 beside each HAProxy
3.4.7 process. It preserves each node's config in its own named volume, uses
DPA's master-runtime reload, and leaves the agent without config write access.
Both cloud and standalone use this identical adapter and operation protocol.
Run `python3 scripts/configuration.py` from the repository root to exercise saves,
weighted routing, proxy/agent restarts and rollback against both nodes. It
restores the original settings after testing.

For an existing node, provision DPA using the
[official installation guide](https://www.haproxy.com/documentation/haproxy-data-plane-api/installation/install-on-haproxy/)
and restrict it to a Unix socket. Make DPA the only writer of one explicit
HAProxy config file, configure its validated reload method, and add these
agent flags alongside the existing enrollment/metrics settings:

```sh
--runtime-socket /run/haproxy/admin.sock \
--config-file /etc/haproxy/haproxy.cfg \
--dataplane-socket /run/haproxy/dataplane.sock \
--dataplane-credentials-file /run/secrets/dataplane-credentials \
--allow-configuration-control
```

The private credentials JSON has `username` and `password` fields; use a
node-specific password of at least 16 characters. Mount that file privately
(readable by the agent UID), both Unix sockets, the config file read-only and a
persistent agent state directory. DPA needs write access to its config and
transaction/backup directories, plus local rights to reload its own proxy.
Do not expose Runtime API or DPA on a public TCP listener. Existing agents
without DPA continue to report metrics and use separately enabled runtime
controls. The enrollment guide still installs an observation agent; it does
not silently assume ownership of an existing node's configuration.

The editor supports backend pools with literal DNS/IP addresses and round robin,
least connections or first available balancing. Existing other backend/server
fields are preserved. Raw rules, frontends, TLS/certificates, include trees,
server templates and upstream traffic handover are subsequent slices.
Rollback restores the full node-local config, including fields outside the
editor. Back up the agent's private `configuration-artifacts/`,
`configuration-bindings/`, `configuration-operations/`, `configuration-receipts/` and identity together;
manager history contains only safe settings and cannot replace missing secrets.
Snapshots cap at 256 files / 128 MiB total / 1 MiB per file. Each intent/receipt
directory caps at 4,096 files. Storage pressure blocks further changes without
pruning rollback history. Starting with rc.12, a raw restore requires a complete
local observation binding that snapshot to its settings and certificate/log
dependencies. The agent checks those dependencies against current local grants
before validation and again before commit. Older snapshots without a binding
remain on disk but are unavailable for restore. The currently loaded snapshot
is bound on its first complete rc.12 observation; new saves capture a bound
starting point. Do not delete state or re-enroll to recover missing history.
See the [DPA evidence and supported profile](providers/haproxy-dataplane.md)
for concurrency, error, verification and recovery details.

## Rolling cluster updates

In a node's **Persistent configuration → Edit backend** form, select **Roll out
to all nodes**, then **Save and roll out**. This starts the update directly under
`haproxy.manage`, the existing shared-login/MFA session and each agent's local
configuration policy. The cluster's **Rolling configuration** section shows the
current node, health window, recovery and the latest 20 updates. The standalone
operator API uses the same Go coordinator and store without Converge.

The console requires an enrolled, managed HAProxy entrypoint and targets every
active node in a cluster of 2–12 nodes. Backend names must match; the supported
backend document is applied uniformly while other file settings remain specific
to each node. Admission captures all node preconditions and the entrypoint binding,
generation, artifact and DPA version in one transaction. The traffic and entrypoint
clusters stay fenced against overlapping runtime/configuration changes, topology
edits and standalone maintenance for the whole update.

Each node runs this sequence in name/ID order:

1. Put its entrypoint server in runtime maintenance, then persist that withdrawal
   through DPA. This also prevents old entrypoint workers selecting it after reload.
2. Wait for the declared traffic frontend to report zero active connections for
   three continuous seconds. Unknown and stale counts never mean zero.
3. Apply and verify its persistent configuration through the existing agent writer.
4. Require five seconds of fresh passing HAProxy health checks on the target and
   healthy peers, then persist rejoin and restore runtime readiness if needed.
5. Verify the target and entrypoint for another five seconds before advancing.

Every enabled, positive-weight backend server must be ready, use its configured
weight and report a completed passing HAProxy check (`L4OK`, `L6OK`, `L7OK` or
`L7OKC`); frontends must be open. Missing observations reset health windows.
Agents must allow both Runtime API and persistent configuration control on the
entrypoint, and target agents must report frontend connection counts. Bypass
traffic can keep a node from draining. No session is forcibly closed.

If an update or health gate fails, changed nodes recover in reverse order to
private starting snapshots, creating new versions. A previously rejoined node
is withdrawn and drained again before rollback. Each recovery node rejoins and
passes verification before the preceding node is touched. Unsent commands can be
cancelled; ambiguous dispatched commands must settle before compensation and
are never replayed. Browser closure and manager restart preserve operation IDs,
maintenance checkpoints and progress.

A drain that exceeds its deadline (30 seconds by default) or a recovery that
cannot be verified stops the rollout as **Recovery needs attention**. Its maintenance
checkpoint is atomically handed to a standalone reservation. Select that
node and use **Node maintenance** to inspect its state, retry drainage, repair
its configuration or explicitly rejoin it with fresh preconditions. The remaining
serving peer is not withdrawn to continue a failed recovery. External entrypoint
or target artifacts are never overwritten. Forward work and reverse recovery are
each bounded to 15 minutes, with bounded node/entrypoint operations and health
windows. An uncertain operation or missing snapshot can require operator repair.

Health comes from observed HAProxy frontends and configured server checks, rather
than traffic rates or declared URL probes. Only the explicitly bound entrypoint
backend and declared traffic frontend are managed. The pair's single entrypoint
remains a lab failure domain. Existing standalone API integrations without a
managed entrypoint retain their original health-gated rollout behavior; they do
not claim upstream withdrawal.

Run `python3 scripts/rollout.py` after `python3 scripts/pair.py up`. It verifies real streaming
requests finish before reload, peer-only routing during drain, ordered rejoin,
weighted traffic without failed GETs, manager restart with unchanged operation
IDs, failed health rollback, withdrawal before restoring an earlier node and a
real drain-timeout reservation followed by explicit rejoin.
It restores the demo's original backend settings. Go/PostgreSQL contracts also
cover concurrent coordinators, timeout handoff, stale/unknown commands, external
changes, permission/CSRF gates and tenant isolation.

API:

- `GET /v1/clusters/{cluster}/rollouts` requires `haproxy.read` and returns the
  latest 20 tenant-bound rollout records, including per-node maintenance phases,
  connection counts, operation IDs and recovery reservation IDs.
  Handed-off checkpoints show the referenced reservation's current recovery state,
  even after it leaves the latest maintenance-history page. The stored rollout
  outcome and starting snapshots remain immutable.
- `POST /v1/clusters/{cluster}/rollouts` requires `haproxy.manage`, browser CSRF
  when applicable, `request_id`, a supported `document`, and all target `nodes`
  with `node_id`, `expected_generation`, `expected_artifact_id`, `expected_version`.
  For a managed cluster, `entrypoint` requires those same fields for the controller
  plus `binding_id` from `GET /v1/nodes/{node}/maintenance`.
  `drain_timeout_seconds` defaults to 30 and accepts 5–300. Identical retries return
  the same rollout; changed membership, settings, entrypoint or timeout conflict.
  Runnable work is limited to 16 operations per installation and eight per
  organization, shared by rollouts and maintenance, including resumed work.
  Held maintenance and recovery-attention reservations keep their safety fences
  but do not consume runnable capacity. Each organization may retain up to 16
  nonterminal maintenance reservations before new withdrawal is refused. Resolve
  existing reservations through the maintenance flow; never delete their records
  to free capacity. Workers select runnable jobs separately from a rotating batch
  of up to 16 held/attention checks, preserving continuous drain/health windows.

Migration 9 adds the optional tenant-bound entrypoint cluster reservation and
immutable entrypoint baseline to rollout records. Existing history is preserved;
startup applies the additive migration under the existing migration lock.

## Configuration discovery and direct runtime control

Select a demo node in the console. **Configuration and runtime control** shows
frontends, backend pools, server health/state, connections and runtime weights.
Use a server’s menu to **Drain server** or **Restore server** immediately. **Change
weight** opens an editor; **Save weight** applies it directly. `haproxy.manage`
authorizes writes; `haproxy.read` authorizes discovery and operation history.
Shared-login writes use the existing recent strong MFA session and Go CSRF checks.
There is no separate approval or configuration adoption workflow.

These operations affect **application servers inside the selected HAProxy node**.
They do not withdraw that proxy from its upstream entrypoint. Drain disables
normal balancing, preserves existing connections, and waits up to 30 seconds
for active connections to reach zero. Persistence rules may still route new
connections to a drained server. Restore sets `ready`; health checks and weight
still determine eligibility. Weight is an absolute integer from 0 to 256: 200
gets twice the normal share of 100. The manager and agent reject drain/weight-zero
if no other healthy, ready, positive-weight server remains in that backend.

Runtime controls are temporary and can reset on HAProxy reload. Their operation
table is separate from the persistent configuration versions described below.

For an existing installation, expose a private local Unix Runtime API socket in
HAProxy’s `global` section, readable/writable only by the proxy and agent group:

```text
stats socket /run/haproxy/admin.sock mode 660 level admin
```

Give the agent access to that socket and read-only access to its relevant
configuration files. Add these flags to the ordinary enrolled agent command:

```sh
--runtime-socket /run/haproxy/admin.sock \
--config-file /etc/haproxy/haproxy.cfg \
--allow-runtime-control
```

Omit `--allow-runtime-control` for observation only. Control requires at least
one explicit configuration file and currently supports the tested HAProxy 3.4.x
profile. Discovery uses `show info`, `show stat`, and `show servers state`;
HAProxy 3.x versions with the required fields can be observed. The tested image
is `haproxy:3.4.7-alpine3.24`. This is runtime resource discovery, not a full
configuration-file editor or dependency parser. Files contribute only a SHA-256
fingerprint; raw configuration, certificates and secrets stay on the node.
List all relevant files explicitly; external reloads/process changes invalidate
stale operation preconditions. Limits are 16 regular files of at most 1 MiB each,
128 proxies, 256 backend servers and 96 KiB of JSON per observation.

The agent opens an outbound HTTP/2 gRPC/Protobuf stream over mTLS to the manager’s
existing TLS listener. A reverse proxy in front of the manager must support
streaming HTTP/2 gRPC upstream and allow the RPC path
`/converge.haproxy.control.v1.Control/Connect`. Do not expose the local Runtime API
or route this machine stream through Phoenix or the browser proxy. Control and
metrics use separate connections, goroutines and concurrency budgets.

One runtime mutation per node is allowed at a time. Every request carries a
unique idempotency key and expected generation, server state and weight; a
stale or concurrent change returns conflict instead of applying to new state.
The manager durably records dispatch and audit history. The agent fsyncs intent
before execution and keeps a bounded private journal (64 entries, at most 1 MiB
on load). Reconnects fence older controller sessions; interrupted operations are
reconciled from fresh runtime state rather than blindly executed again.
An unverified outcome stays **Outcome unknown**, never assumed success.
The agent reports every second; console discovery/history polls every two seconds
while visible. Control freshness expires after six seconds independently of
telemetry freshness. HAProxy continues serving through manager or agent outages.

`python3 scripts/pair.py operations` tests both nodes against routed application traffic:
drain/restore, the last-server guard, 200:100 weighted distribution, duplicate
requests, and agent/manager restarts. It restores both application servers to
ready/weight 100 even on failure. Unit/integration tests additionally cover
in-flight restart reconciliation, generation conflicts and certificate revocation.

## Whole-node maintenance

The **Node maintenance** panel withdraws an application HAProxy node from its
managed shared entrypoint. It applies directly with `haproxy.manage` and the
entrypoint agent's runtime/configuration opt-in; there is no review or approval.
The console shows withdrawal, live connection drain, the maintenance reservation
and rejoin health checks. The cluster topology and maintenance history show the
same durable state. Closing the page does not cancel an authorized operation.

The first supported upstream is another enrolled HAProxy, running the same Go
agent beside its private Runtime API and DPA. Put that entrypoint identity in a
separate management cluster. Add an explicit `control` object to the application
cluster's `PUT /v1/clusters/{cluster}/topology` metadata:

```json
{
  "node_id": "<enrolled-entrypoint-node-uuid>",
  "backend": "haproxy_nodes",
  "servers": [
    { "node_id": "<application-haproxy-a-uuid>", "server": "haproxy-a" },
    { "node_id": "<application-haproxy-b-uuid>", "server": "haproxy-b" }
  ]
}
```

These must be same-tenant enrolled identities and literal HAProxy backend/server
names. Every server in the managed backend must be mapped. The display URL and
node addresses never become API destinations. The local pair installs and binds
an entrypoint agent automatically; it remains a single entrypoint/host failure
domain. Provider VIPs, VRRP/Keepalived and arbitrary upstream HTTP APIs are not
part of this profile.

**Start node maintenance** first sends runtime `state maint` to block all new
server selection without killing existing connections. It then writes the
server's persistent maintenance flag through the existing DPA revision/reload
path. The runtime step precedes reload so old workers cannot continue selecting
the target. Withdrawal survives an entrypoint restart. Fresh target observations
must report zero connections on the declared traffic frontend for three seconds
before the node becomes **In maintenance**. Legacy agents without this optional
connection count cannot enter maintenance. Direct/bypass traffic can keep this
frontend busy; other frontends are not part of the drain guarantee.

The reservation stays in place until **Rejoin traffic** is chosen. While held,
you can edit/reload the target's backend configuration. Peers and the entrypoint
stay fenced, and standalone maintenance cannot overlap a cluster rollout. Managed rollouts embed
the same maintenance sequence under their own reservation. Rejoin captures
the target's current generation and requires five seconds of fresh healthy
observations, including healthy peers. It then enables the saved upstream server,
restores runtime selection if needed, and requires another five seconds of
healthy target and entrypoint observations. Entry health checks get up to twenty
seconds to settle. A failed rejoin automatically starts withdrawal again.

A drain deadline (30 seconds by default, configurable 5–300 through the API)
leaves **Needs attention**, with withdrawal and the cluster reservation retained.
It never kills sessions or silently rejoins. Inspect the recorded error, let
connections finish and retry withdrawal or rejoin. Each command is bounded to
30 seconds and a transition to two minutes. Unknown writes are not replayed;
unverified outcomes, unavailable control, binding changes or external entrypoint
artifact/address drift block continuation. Restore the expected entrypoint state
before retrying. The global worker admits up to sixteen reservations, one per
involved cluster, and locks both clusters in UUID order across manager replicas.

API:

- `GET /v1/nodes/{node}/maintenance` (`haproxy.read`) returns the exact topology
  binding digest, target/entrypoint observations and latest maintenance record.
- `POST /v1/nodes/{node}/maintenance` (`haproxy.manage`, browser CSRF) accepts a
  UUID `request_id`, `action` (`withdraw` or `rejoin`), optional `maintenance_id`
  (required for rejoin/retry), `binding_id`, target `expected_generation`,
  `expected_entrypoint_generation`, full `expected_artifact_id`, DPA
  `expected_version` and `drain_timeout_seconds`. Stale preconditions return 409.
  Retries with the identical request return the same durable reservation.
- `GET /v1/clusters/{cluster}/maintenance` (`haproxy.read`) returns the latest
  twenty reservations, including completed history. Actor attribution and stage
  transitions are recorded in append-only audit history. Both deployment modes
  use the same code and independent storage.

Run `python3 scripts/maintenance.py` after `python3 scripts/pair.py up`. It holds real requests
through the shared entrypoint, verifies surviving traffic, tests persistent
withdrawal across entrypoint/manager/agent restart, rejects unhealthy rejoin and
checks a short drain deadline without terminating its live request. It finishes
with both nodes serving traffic. Contracts also cover cross-tenant endpoints,
CSRF, permissions, exclusive coordinators, immutable history, missing connection
counts and automatic isolation after rejoin health failure.

## Observation limits

The manager accepts at most eight control opens/reports per second per enrolled
node. A reconnect does not reset that process-local budget. Normal agents report
once per second; metrics and request telemetry use their separate bounded paths.

Automatic configuration discovery retains at most 1,024 versions per node.
Afterward, the latest settings and metrics continue updating, and the console
shows that automatic history is full. Saved versions remain available. An
authorized edit or rollout still captures its current rollback base and records
the requested change; observation-only reporting cannot bypass the limit.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
