# HAProxy load, failure and recovery drills

These operator-side tests exercise the customer-owned two-node Docker demo.
They do not enable HAProxy approvals or write DNS. They use the independent
operator API; Converge login, Phoenix and Converge's database are not involved.

## Run

Start the existing two-node demo with both synthetic applications and verified
HTTPS first. See the [demo instructions](development.md#real-two-node-demo).
Finish active deployments/maintenance before running a drill. Do not run other
demo mutation scripts concurrently.
The runner refuses remote Docker contexts/host overrides; its fault injection
is restricted to a local Docker Unix socket and the synthetic demo project.

```bash
export HAPROXY_PAIR_STANDALONE=1
make hardening
# Individual phases use the same preflight and final invariants:
python3 scripts/hardening.py load
python3 scripts/hardening.py failures
python3 scripts/hardening.py restore
# Independently enforce collection-to-SSE latency while load continues:
python3 scripts/hardening.py load --live-metrics
# A longer fixed-rate run; this measures the current machine, not product capacity:
python3 scripts/hardening.py load --seconds 300 --rps 200 --workers 16
```

If the existing lab belongs to another checkout, set `HAPROXY_DEMO_ROOT` to that
checkout's absolute path. This selects its Compose files and private state;
identities and keys are not copied. Keep `HAPROXY_PAIR_STANDALONE` consistent with
the running lab (`0` for the optional Converge development-login overlay).
Complete source builds and database tests before the stress run. Their compiler
and storage load must not compete with a capacity measurement in a small VM.
Stop additional console/connector instances during a full recovery drill so the
checkpoint's writer pause includes every manager using that demo database.
For a small shared development VM, put the disposable restore on a separate
local Docker engine with `--restore-context local-qa`. The source keeps its
current context. The target must use a local Unix socket and already contain the
exact source images; missing images fail before the cold control-plane shutdown.
The restore network remains internal, and the report records both memory budgets.

The default workload is 80 requests/second with eight workers for 30 seconds.
`--live-metrics` adds three live-feed latency trials under ongoing traffic and
fails above five seconds; it does not flush the storage engine to force a pass.
Both apps,
both nodes and verified HTTP/HTTPS must participate. Workers have bounded
concurrency, a three-second socket timeout and no catch-up bursts. Latency uses
a bounded millisecond histogram. A run fails for serving errors, p95 above the
default two-second budget, less than 80% of requested throughput, missing route
coverage, stale post-recovery metrics/configuration, changed application history
or an out-of-memory container death. Override the latency budget explicitly with
`--max-p95-ms`; do not treat a lower offered workload as a capacity result.
When request telemetry is enabled, steady load also requires fresh collection
from both agents and no collector restart, rejected/dropped events or unconfirmed
deliveries. Counter deltas are recorded separately from intentional outage phases.

`deploy/local/state.local/hardening/latest.json` contains sanitized per-phase
counts, latency, route distribution, resource samples, failover/recovery times
and restore evidence. Use `--report /absolute/path.json` for another destination.
The CI hardening job prepares its own standalone lab and retains only this report.
Run workload measurements separately from compilers and other memory-heavy jobs.
Use separate runners or allocate and isolate enough memory before drawing
capacity conclusions.

## Local acceptance results (2026-10-29)

Measured on the two-node demo in a Docker VM with two CPUs and 2,054,832,128 bytes
of memory. The disposable restore used a separate two-CPU VM with 4,095,635,456
bytes of memory. Workloads, restart smoke tests and repository verification jobs
ran separately:

| Scenario                                                           | Result                                                                                                                                                                          |
| ------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Default combined load, component outages, crash/rejoin and restore | 18,939 source requests, zero serving failures; steady 79.96 requests/s, 13 ms p95                                                                                                |
| Five-minute workload at 200 requests/s                             | 60,091 successful steady requests; 199.93 requests/s, 15 ms p95; no recorded collector rejects, drops or unconfirmed deliveries                                                   |
| Strict live feed under 200 requests/s                              | Both nodes reported each burst in 0.51, 1.00 and 1.00 seconds; the combined 80 requests/s drill measured 1.00, 1.50 and 1.00 seconds                                             |
| Abrupt node A loss                                                 | Verified convergence onto node B in 4.42 seconds; returning node warmed while drained                                                                                           |
| Cold restore                                                       | 69.92 seconds including checks/restart; 80 restored HTTP/HTTPS requests; original identities and certificate pins, 1,644 historic metric points and 20 request records verified |

The recovery guard tests also exercise damaged/incomplete backups, missing keys
and images, unsafe archives, remote Docker contexts, collector loss and writer
resumption after copy failure. The runner checks image availability before
pausing writers and drains a returning node through backend warm-up. The isolated
live-feed result above does not establish an unconditional latency guarantee.
Use this repository's current CI results for source-check status; Converge API
and frontend test counts are separate from Ecraela Fleet qualification.

## Failure matrix

The suite stops and recovers the manager, ingestion, PostgreSQL, VictoriaMetrics,
VictoriaLogs and node A's agent in turn while both applications continue serving.
Every recovery must produce new successful metrics and configuration discovery;
an old database observation does not qualify. An abrupt node A process death is
tested separately. In-flight requests may fail during health-check detection;
the report preserves those failures and requires convergence onto node B within
ten seconds. After convergence, the ordinary zero-serving-error budget applies.
The returning node is held in runtime drain until all of its backend checks
pass for five seconds, then restored through the normal permission-authorized
control stream. The entrypoint's stats-port liveness check alone is insufficient
to establish application readiness after a crash.
Node A must return to the pair before the test finishes. A management or storage
outage never justifies calling stale observations current or successful.

## Checkpoint contents and restore proof

The checkpoint briefly pauses management, ingestion, all three demo agents,
metrics/log stores and the local DNS-01 fixture's writer. Serving HAProxy nodes
and application backends remain running. No configuration change is accepted
during this window. PostgreSQL is captured using `pg_dump -Fc`; its raw volume
is never copied. Cleanly stopped metrics/log volumes are copied with their
existing retention. The same recovery point includes:

- Installation CA/signing key, server keypair and tenant operator credentials.
- Each of the three demo agents' identity certificates/private keys, runtime journals, raw configuration
  artifacts and configuration-operation recovery state.
- Persistent HAProxy/Data Plane configuration and credentials, local authority
  policies, TLS certificates/keys and node-local ACME account/order state.
- The demo entrypoint configuration, identity and local DNS-01 fixture material.

Runtime sockets are excluded and recreated. Required shared group permissions
and numeric owners are preserved. A failed checkpoint resumes original writers
in `finally`; it never marks an incomplete checkpoint complete.

Restore checks file digests and required keys before creating containers. It
rejects links, special files, unsafe archive paths and privileged executable
modes. These checks detect damage; the manifest is not a signature and backups
are trusted operator material, not arbitrary uploaded archives.
Each volume archive is bounded to 100,000 entries and 2 GiB of file data in this
local drill; larger production backups need a separately qualified workflow.

During the cold-recovery rehearsal the source management/telemetry services and
their PostgreSQL stay stopped, bounding resource usage and proving serving
independence. The original HAProxy nodes, backends and entrypoint continue serving;
the source console/telemetry are temporarily unavailable. Source control services
resume in `finally` after the disposable restore is removed.

The drill restores into a randomly named Compose project with fresh database,
metrics, log, configuration and certificate volumes. Its Docker network is
internal, with no published management/traffic ports; probes run inside it through
Docker exec. Browser login
federation is disabled. The restored identities cannot contact the source
manager or external notification services. Enrollment-token mounts are omitted:
nodes must authenticate using their saved key and existing enrollment.
The checkpoint pins the source containers' exact local image IDs; moving a
development image tag during the run cannot silently change the restore binaries.
Fresh-storage restore waits for PostgreSQL's final TCP listener. The temporary
Unix-socket server used during image initialization can report readiness before
the target database exists; its response cannot admit a checkpoint import.
Missing/pruned source images fail preflight before writers are paused. Retain
the supported release images as part of recovery operations; a running container
alone does not prove its original image can still be started.

Before reconnecting node A, the test removes its key from the disposable identity
copy and verifies startup fails without creating another identity. It restores
that key, reconnects all agents, requires new metrics/configuration, verifies
application version/rollback history and historic metrics/request records, and
serves both apps over HTTP/HTTPS on both nodes with the original certificate
pins. Before the serving checks, both restored nodes and the entrypoint must
report healthy backends continuously for five seconds. Agent connection alone
does not prove HAProxy's startup health checks have finished.
Manager/agent restart must work again afterward. The source identities,
application configuration and history must remain unchanged throughout.

Default cleanup removes the disposable project, volumes and plaintext checkpoint.
Only the sanitized report remains. If Docker cleanup fails, the report records
`cleanup_required`; the private workspace is retained so its Compose file can be
used to remove that exact disposable project. Never use a source-project volume
deletion as recovery cleanup.

## Production boundaries

This is a repeatable local recovery rehearsal, not an automated backup service or
a production capacity certification. Production backups require encrypted,
access-controlled storage on another host/failure domain, retention, integrity
authentication and scheduled restore exercises. Node keys remain operator-owned;
the manager has no key upload/download API. Database-only recovery cannot
reconstruct node identities, ACME accounts or serving keys.

The local ACME CA is a fixture; its upstream registration/order database is not
recreated by copying node account files. Production recovery must retain access
to the original issuer and reviewed DNS delegation. The drill proves saved TLS
serving material and node keys work; renewal qualification remains covered by
the separate `python3 scripts/acme.py smoke` scenario. It does not prove fresh public
DNS reachability, remote-host loss, provider VIP failover, external notification
delivery, hardware-sized throughput, long-term retention or compatibility across
different manager/agent/HAProxy versions. The one local entrypoint/host is still
a shared failure domain. Linux release packaging and upgrade qualification remain
separate release gates.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
