# HAProxy Data Plane API evidence and supported profile

Application verification extension (2026-10-26): [ADR 0042](../adr/0042-haproxy-application-rollout-verification.md)
adds direct node-local HTTP/HTTPS gates around the existing maintenance/DPA
sequence. It uses the same pinned DPA transaction and reload contract, with no
new DPA endpoint or permission. Version 3 agent discovery carries safe local
grants and artifact/generation-bound request results; the independent Go
coordinator freezes authority and verifies both deployment and restoration.
Shared-entrypoint observations remain separate. The real pair smoke injects a
healthy backend with the wrong app response over each protocol and checks that
the serving neighbor and shared TLS remain intact.

Lifecycle extension checked 2026-10-25: [ADR 0041](../adr/0041-haproxy-application-lifecycle.md)
uses the same pinned DPA full-section backend PUT and DELETE operations, inside
the existing configuration transaction. Disabling retains ordered routing and
writes an empty owned backend, preserving original endpoints only in typed safe
intent. The [official HAProxy HTTP status reference](https://docs.haproxy.org/3.4/configuration.html#1.4.1)
defines 503 when no server is available. Removal updates only the selected app's
managed routes and deletes its backend; shared binds/TLS and other backend
sections are preserved. Empty backend fields after cold parse are normalized
explicitly, and discovery rejects any unexpected upstream server. The real
two-node lifecycle smoke checks these behaviors over HTTP and trusted HTTPS.

Contract checked on 2026-10-25. This adapter belongs to the
independent Go manager/agent; it does not use Converge's DNS approval lifecycle.

## Official contract

- [Data Plane API v3.4.5 release](https://github.com/haproxytech/dataplaneapi/releases/tag/v3.4.5), released 2026-10-21.
- [Pinned OpenAPI specification](https://raw.githubusercontent.com/haproxytech/dataplaneapi/v3.4.5/specification/build/dataplane_spec.yaml), Apache-2.0.
- [Installation](https://www.haproxy.com/documentation/haproxy-data-plane-api/installation/install-on-haproxy/) and [configuration reference](https://www.haproxy.com/documentation/haproxy-data-plane-api/reference/configuration-file/).
- Pinned [raw handler](https://github.com/haproxytech/dataplaneapi/blob/v3.4.5/handlers/configuration/raw/raw.go), [backend handler](https://github.com/haproxytech/dataplaneapi/blob/v3.4.5/handlers/configuration/backend/backend.go), and [reload implementation](https://github.com/haproxytech/dataplaneapi/blob/v3.4.5/reload_agent/reload_agent.go).

The pinned upstream specification above is 1,239,561 bytes, with SHA-256
`d710b569538af40d44ac01185a13a0c5734da7efcbe346bcbbf5d670f0b6b07d`.
Verify a downloaded copy against this digest before using it as contract
evidence. The adapter uses a small hand-written allowlist projection while retaining
unknown local JSON fields; the spec and protobuf generation have separate pins.

## Authentication and local ownership

The tested blueprint uses HAProxy 3.4.7 and DPA 3.4.5. Other DPA 3.4.x versions
can be observed but require the same contract tests before deployment. DPA binds
only its Unix socket and uses a dedicated local BasicAuth user; it has no
per-resource capability scopes. Give this user access only to its one proxy.
Never publish the socket or a DPA HTTP listener to the Internet. Filesystem group
permissions restrict the socket; credentials are mounted separately to the agent
as a private JSON file. Rotating them requires updating DPA and that file together
and restarting the agent. Raw credentials never enter the manager or console.

DPA owns its configuration and uses HAProxy's master runtime socket for reloads.
The pair runs both processes under UID 10001, without root, additional running
capabilities, shell commands supplied by users, or Docker socket access. A small
supervisor forwards termination and restarts a failed DPA process. The separate
agent has read-only access to the proxy configuration and cannot rewrite it.
Persist the proxy config/DPA state volume and the agent identity/history directory.
An external administrator must also use DPA: the DPA version, content hash and
runtime generation protect concurrent edits but do not fence arbitrary filesystem
writers or another controller racing after validation.

## Supported edits and bounds

The console edits existing backend pools: round robin, least connections or first
available; literal DNS/IP addresses, ports, integer weights 0–256, server addition
and removal, and persistent maintenance. Existing server names are stable.
Backend names cannot be added, renamed or removed through this profile. Existing
unknown backend/server fields and frontend/global/resolver settings are preserved
by full-section transactional updates. Newly added servers receive a health check.
Parameterized balance algorithms, server templates, empty pools, unsupported
server addresses, and configurations outside the profile remain read-only.
Raw configuration, ACL/routing rules, certificates, maps, includes, dynamic
provisioning are outside this editing slice. The separate cluster coordinator
uses these same node transactions for sequential rollout and automatic recovery.
The supported blueprint uses one explicit configuration file.

Reports contain at most 32 backends, 128 servers total and 16 KiB of safe settings.
Each local raw snapshot is at most 1 MiB; the agent retains at most 256 snapshots
and 128 MiB total. Intent and commit-receipt directories each cap at 4,096 files.
The private `configuration-bindings/` directory holds at most 256 records of
128 KiB each. A record binds a raw artifact to its canonical settings, actual
certificate paths, request-log profile and compiled traffic-rule dependencies,
captured only by a complete,
DPA-version-fenced observation. A command or commit receipt cannot create that
authority. Raw restores require this record and recheck current TLS, request-log
and traffic-policy grants before validation and before commit. Missing, corrupt
or mismatched records fail closed; raw bytes remain untouched. Snapshots predating
rc.12 need an independent local observation before they can be restored. The
current snapshot is observed automatically after upgrade; new rollback bases
are captured before edits. The raw DPA endpoint cannot parse an inactive candidate
into an independently verifiable projection, so unbound historical data is not
accepted as a substitute.
Reaching a limit disables further saves instead of deleting rollback history.
Back up these private directories with the node identity; cloud history alone
cannot reconstruct secrets or a deleted full snapshot. There is no automatic
history garbage collection in the current profile.

## Concurrency, verification and errors

A manager save requires `haproxy.manage`, local configuration-control opt-in,
fresh discovery, expected runtime generation, DPA version, full content artifact
and one pending operation per node. The manager records an immutable revision,
operation, actor and audit event atomically. Reusing its request ID returns the
same operation; a different payload or rollback target conflicts. History is
append-only; a restore is a new forward version. Current history responses are
bounded to 50 entries including the current version. Agent discovery polls each
second and console history polls every two seconds while visible; DPA collection
and telemetry have separate paths. Backend listing has no pagination.

Normal edits use `POST /transactions?version=N`, full backend PUTs within that
transaction, candidate raw inspection, then a forced transaction commit. DPA
validates with the installed HAProxy binary before reload. Restores validate and
post a private full snapshot with a version precondition. Raw GET without a
version reads the current file: specifying a version selects a backup. DPA
rewrites metadata and formatting; content identity ignores only its version and
MD5 comment lines. A raw restore stores DPA's returned full configuration in a
separate durable commit receipt. A lost commit response remains unknown.

Success requires a fresh runtime observation, a new worker PID, matching saved
projection, loaded weights/algorithm/server membership, a healthy positive-weight
server per pool and the exact resulting full artifact. A receipt or pre-commit
intent never causes mutation replay after reconnect/restart. Unknown outcomes
require checking the current settings; failed reloads are reported honestly.
DPA has its own last-known-good reload recovery. Individual node saves record
health verification failures for the operator; cluster updates add automatic
compensation. There is no atomic two-node deployment guarantee. Cluster rollouts use a
five-second manager-timed health window, checking both `UP` state and completed
`check_status` values from the [official Runtime API statistics](https://www.haproxy.com/documentation/haproxy-runtime-api/reference/show-stat/)
(retrieved 2026-10-25), then compensate failures with new per-node restores. A full restore also restores
that node's non-editor settings from the snapshot.

Local HTTP calls time out in three seconds; operation verification has a 60-second
deadline. 406/409 map to configuration conflict; 401/403, throttling and 5xx map
to configuration unavailable. Rejections, unsupported profiles, missing snapshots,
local storage pressure and reload failures use bounded codes. Provider response
bodies are never exported. There is no advertised DPA quota used here; bounded
connection counts and one command worker limit load, and mutations are never
blindly retried.

## Validation

On the authorized two-node Docker lab, run `python3 scripts/configuration.py` from this
repository root. It tests each node independently with continuous routed GETs,
2:1 weighted distribution, duplicate and stale requests, HAProxy/agent restarts,
strategy changes, server additions, maintenance and a rollback that survives
another restart. It restores baseline settings afterward. `python3 scripts/pair.py operations`,
`python3 scripts/pair.py failover` and `python3 scripts/pair.py latency` cover the existing runtime,
availability and telemetry behavior. Go unit/race and PostgreSQL contracts check
tenant isolation, permissions, CSRF, immutable history and interrupted recovery.
Last successful local contract testing: 2026-10-25.

## HTTP application profile (2026-10-25)

ADR 0036 extends this pinned contract with `POST /configuration/backends` and
full-section `PUT /configuration/backends/{name}` / `PUT /configuration/frontends/{name}`
inside the same `/services/haproxy/transactions` transaction. Full frontend children
use `backend_switching_rule_list`; rules carry `name`, `cond: if` and `cond_test`.
Inline conditions use exact `req.hdr(host),lower,field(1,:) -m str` and literal
`path_beg`, ordered by decreasing path-prefix length. Hostnames and paths pass
strict typed validation before compilation; arbitrary expressions are never input.
The adapter preserves listener binds, other rules and the existing fallback.

After a cold start DPA reports a backend's inherited local defaults as `from`,
including `unnamed_defaults_1` for an initially unnamed section. That inheritance
stays outside application intent: discovery permits this known parser field,
new applications use their frontend's local defaults, and application edits
preserve the backend's existing defaults. Other unknown managed-section fields
remain blocking drift. Restart and edit contract tests cover this behavior.

Managed backend descriptions record a bounded base64 encoding of safe typed
application intent. Discovery verifies actual backend fields, `adv_check: httpchk`,
`httpchk_params.method/uri`, enabled server checks and ordered frontend rules.
Health settings use GET, 1,000 ms interval/check timeout and rise/fall 2. Managed
backend mutation and routing activation commit/reload together. A changed app
revision recompiles only its owned backend; neighbours retain their sections.
Full saved/runtime artifact verification and existing unknown-outcome rules apply.
No new credentials, raw-config transport or external API exposure are introduced.

The official pinned schema linked above defines the full-section and health-check
fields. [Official ACL guidance](https://www.haproxy.com/documentation/haproxy-configuration-tutorials/proxying-essentials/custom-rules/acls/)
was checked on 2026-10-25. `python3 scripts/applications.py` is the real-proxy acceptance
scenario; it must pass alongside the unit/PostgreSQL and API authorization checks.

HAProxy 3.4's [management guide, CSV format](https://docs.haproxy.org/3.4/management.html#9.1)
defines `* L7OK` as the last known passing result while the next check is running.
The manager and console strip exactly the `* ` marker for status classification;
raw observations retain it. Unknown, initializing and failed last results remain
non-passing, including `* L7TOUT` and `* L7STS`. This avoids false rollback on a
healthy in-progress probe without changing readiness, freshness or health windows.
Regression tests cover marked passing and failing results.

## Node-local HTTPS profile (2026-10-25)

ADR 0037 adds a named bind through the existing full-section frontend transaction
API. The pinned schema defines `binds`, `ssl`, `ssl_certificate`, `ssl_min_ver`,
`alpn`, `strict_sni` and `force_strict_sni`. DPA 3.4.5 reports strict SNI using
both `strict_sni: true` and `force_strict_sni: enabled`; projection verifies that
normalized pair. Bind certificate paths come only from local agent policy.
Private material never enters management requests or telemetry. Immutable path
rotation avoids HAProxy's certificate-path cache retaining an earlier leaf.

Owned `http_request_rule_list` entries use `set-var` with `var_scope: txn`,
`var_name`, `var_expr`, `cond` and `cond_test`, followed by `return` with
`return_status_code: 404`. Conditions combine `ssl_fc`, exact lowered
`ssl_fc_sni`, the existing Host predicate and literal path prefixes. A local
policy grants the bind; agent-side checks verify its currently loaded leaf.

The [HAProxy 3.4 configuration manual](https://docs.haproxy.org/3.4/configuration.html)
documents TLS version limits, strict SNI, certificate loading, request rules and
server destination ports. The real demo uses a TCP entrypoint with servers
without fixed traffic ports and explicit `check port 8404`. DPA exposes that
check as `health_check_port`; the adapter accepts incoming-port forwarding only
for explicit TCP backends with that fixed health-check port. HTTP and HTTPS use
the same server identities and withdrawal controls. The generic backend editor
does not edit this infrastructure-only port profile.

Authentication, local Unix socket exposure, request bounds and error handling
remain the same. `python3 scripts/listeners.py` exercises the contract against the pinned real
HAProxy/DPA pair; unit, race, PostgreSQL and tenant/CSRF cases cover local policy,
metadata, immutable history and composition. ACME and arbitrary certificate or
frontend management are outside this profile.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
