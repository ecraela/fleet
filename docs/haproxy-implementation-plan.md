# Ecraela Fleet implementation plan

Date: 2026-10-26

Status: Proposed implementation sequence. This document does not mark new
architecture decisions accepted or claim production readiness. Use the
[documentation index](README.md) and current release notes for implemented behavior;
this plan contains proposals that may already have been narrowed or superseded.

Implementation update (2026-10-27): [ADR 0049](adr/0049-haproxy-outbound-converge-connector.md)
adds optional outbound linking with independent local login. [ADR 0050](adr/0050-haproxy-self-hosted-installation.md)
adds signed Linux/Docker installation, compatible upgrades, encrypted signed
manager/node backups and fenced cold restore. See the
[customer installation runbook](self-hosted-installation.md). Teams and MFA are
deferred; production capacity and multi-host management availability remain open.

Implementation update (2026-10-25): [ADR 0040](adr/0040-haproxy-automatic-dns01-tls.md)
accepts the first automatic TLS profile: node-local ACME DNS-01 keys and orders,
scoped RFC 2136 challenge updates, reviewed CNAME delegation, durable renewal jobs,
per-node certificate pins and rolling activation/compensation. The console has
setup, issuance, retry, expiry and generation history in drawers. Wildcards,
provider DNS API solvers, EAB/ARI, additional notification adapters and automated archive
management remain roadmap work. This accepts the narrow ADR, not every proposal
in this broader roadmap.

Implementation update (2026-10-25): [ADR 0041](adr/0041-haproxy-application-lifecycle.md)
adds rolling disable/re-enable, recoverable deletion/restoration and shared
listener/TLS preservation. The console exposes lifecycle state, retained history
and a DNS cleanup review handoff. Cleanup drafts are still edited in the normal
zone editor; there is no automatic DNS removal. General protocol coverage,
hostname migration and backend retirement remain open under A7.

Implementation update (2026-10-27): [ADR 0048](adr/0048-haproxy-standalone-console.md)
adds the standalone embedded console with local administrator/viewer accounts,
server-side sessions, password recovery, existing HAProxy drawers and live charts.
DNS remains instructions through the operator's normal DNS workflow. Browser user
administration and MFA remain separate work; optional linking is now ADR 0049.
See [self-hosted console setup](self-hosted-console.md).

## Outcome and release sequence

Implementation update (2026-10-26): [ADR 0042](adr/0042-haproxy-application-rollout-verification.md)
adds opt-in HTTP/HTTPS node-local request gates before and after rejoin, with
durable evidence and baseline verification during automatic rollback. Every
active verified app in the effective configuration participates.
[ADR 0043](adr/0043-haproxy-entrypoint-rollout-verification.md) adds opt-in
shared-entrypoint HTTP/HTTPS gates after rejoin, including restored baseline
verification. Public request gates and configurable phase policies remain open;
agent-side checks do not prove public reachability.

Implementation update (2026-10-26): [ADR 0044](adr/0044-haproxy-operational-incidents.md)
adds fixed durable application, loaded-certificate, renewal, agent, telemetry and
deployment incidents, a console timeline and opt-in signed HTTPS webhooks.
Debounce/recovery checkpoints and delivery leases live in the Go manager's own
PostgreSQL. Configurable alert policies, browser notification settings and additional
delivery adapters remain open under T3.

Implementation update (2026-10-26): [ADR 0045](adr/0045-haproxy-application-request-telemetry.md)
adds opt-in per-application request collection in private VictoriaLogs, recorded
status/rate and estimated latency views, typed request search, configuration
fingerprints and collection-loss visibility in console drawers. Request content
search, tracing, configurable sampling and measured production capacity remain
open under T3.

Implementation update (2026-10-26): [ADR 0046](adr/0046-haproxy-local-request-path-policy.md)
adds local route-template/exact-path policy, bounded top-path rankings and path
filters in the request drawer. Query strings remain excluded; default collection
withholds unknown paths before transport. Raw content search and production
capacity testing remain open.

Implementation update (2026-10-26): the [hardening drills](haproxy-hardening.md)
add bounded two-app HTTP/HTTPS load, an optional live-feed latency gate, component
failure/recovery and a fenced fresh-storage restore of the manager database,
metrics/logs, node identities, ACME state, configuration artifacts and TLS keys.
These are repeatable local/CI acceptance scenarios, not production capacity or
multi-host availability certification. Encrypted off-host backups and longer
hardware-specific soak qualification remain open.

Implementation update (2026-10-26): [ADR 0047](adr/0047-haproxy-agent-releases.md)
moves the Go manager/agent, independent builds and release lifecycle to private
[`ecraela/haproxy-manager`](https://github.com/ecraela/haproxy-manager), consumed
by Converge as a pinned submodule. Signed exact-version manifests bind Linux
executables and Docker digests. Host-owned installers preserve identities/keys,
retain previous software, verify fresh metrics and recover failed upgrades.
The console reports agent/manager versions separately. The real Docker and
native Linux CI qualification is documented in the
[release runbook](agent-releases.md).
Standalone browser UI/local user login and the optional installation-initiated,
scoped outbound Converge connector are implemented for the pilot. Neither
requires moving HAProxy traffic to Ecraela. The owner selected
[AGPL-3.0-only with optional commercial licensing and paid support](../LICENSING.md)
on 2026-11-03 ([ADR 0060](adr/0060-haproxy-licensing-and-support.md)). The repository
remains private; public distribution is a separate publication step.

Build a control plane that lets a team publish, operate, discover and protect
applications on community HAProxy. Keep HAProxy in the traffic path and keep
Converge management outside it. Deliver four releases in this order:

1. **Application delivery:** hostname/listener, TLS, routing, backends, health
   checks, verified deployment, rollback and optional reviewed DNS publication.
2. **Team operations:** application/cluster delegation, automation identities,
   request investigation, alerts and supported operational packaging.
3. **Discovery:** continuously reconcile explicitly selected Kubernetes and
   Consul services into application endpoints.
4. **Security:** policy profiles, decision telemetry, learning mode and a
   validated WAF integration.

Each release has an executable acceptance scenario and a release gate below.
These are dependency-ordered work packages, not calendar estimates. Estimate
each after its adapter/architecture spike; do not promise dates from this plan.

## Current baseline

The baseline now includes configuration editing, whole-node maintenance, rolling
drain/update/health/rejoin, HTTP applications on shared listeners (ADR 0036), and
the bounded node-local HTTPS profile in ADR 0037, and automatic DNS-01 issuance
and renewal in ADR 0040. The broader release gates below remain open; these
increments do not imply general listener management or every ACME profile.
Application details and locally configured HTTP/HTTPS entrypoint probes are
implemented in ADR 0038. Their reported reachability is location-specific;
public probes remain open. ADR 0042 adds separate direct node-local gates;
ADR 0043 adds opt-in shared-entrypoint rollout gates with exact initiating
artifact/generation correlation, without claiming which upstream served them.
Customers own nodes, workloads, VIPs and provider networking. Ecraela-hosted
traffic infrastructure and machine provisioning are outside this roadmap.

| Area           | Present implementation                                                                                                                       | Limit relevant to this plan                                                                                                      |
| -------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| Manager/agents | Independent Go manager, ingestion role, dedicated PostgreSQL, VictoriaMetrics, outbound mTLS control/telemetry                               | Hosted management and standalone operator API; independent embedded self-hosted console and tenant-bound local login in ADR 0048 |
| Configuration  | Backend edits, shared-listener HTTP applications, local TLS aliases, node-local DNS-01 automation, immutable revisions and private snapshots | No arbitrary frontend creation, key distribution, maps or general DNS provider solver support                                    |
| Rollouts       | Durable sequential 2–12-node drain/update/health/rejoin coordinator, app/listener composition and compensation                               | Agent certificate pin verification is not public reachability or public CA trust evidence                                        |
| Topology       | Declared node/entrypoint bindings                                                                                                            | Declared topology is not proof of reachability                                                                                   |
| Maintenance    | Durable coordinator withdraws/rejoins through an enrolled HAProxy entrypoint                                                                 | No general provider VIP/floating-IP implementation                                                                               |
| Access         | Local administrator/viewer login, `haproxy.read`/`haproxy.manage`, installation operator credentials and optional Converge grants            | Local MFA, application/cluster delegation and customer automation credential lifecycle are deferred                              |
| Visibility     | Bounded live/historical node metrics; durable operational incidents and opt-in webhooks                                                      | Bounded request metadata search; no content search, custom alerts or security pipeline                                           |
| DNS            | Provider-neutral instructions locally; optional Converge application and TLS delegation drafts                                               | Publication and verification stay in the provider's ordinary DNS flow                                                            |

Sources: [ADR 0035](adr/0035-haproxy-management-service.md),
[manager runbook](../README.md),
[DPA profile](providers/haproxy-dataplane.md).

## Architectural rules

- Extend the Go service and its own local identity/tenant model. Optional Converge
  integration retains Converge's organization membership and DNS resources; it
  does not become an HAProxy command, ingestion or query proxy. HAProxy applications are not zone-owned provider
  load balancers from ADR 0033 and can exist without DNS.
- Preserve direct authorized HAProxy actions with local agent policy, audit and
  verification. A read-only deployment preview is useful, but does not introduce
  DNS-style approval, adoption or per-generation acceptance steps.
- Preserve the DNS `import -> compare -> plan -> approve -> apply -> verify`
  lifecycle. A successful application deployment never silently publishes DNS.
- DPA remains the durable configuration writer. Runtime API changes have explicit
  temporary/durable semantics. Managed files, auxiliary assets and Runtime API
  transactions need a single documented writer/ordering contract before use.
- Keep private keys, raw full configuration and secret-bearing rollback artifacts
  on nodes in the first profile. Manager records safe intent, metadata and hashes.
  No private-key upload/download feature is implied by a certificate reference.
- Compile effective node configuration across all affected applications. Serialize
  mutations by cluster/shared listener/node and check full-artifact preconditions.
  App-scoped changes cannot restore another app's configuration behind its owner.
- A deployed configuration and a reachable application are separate observations.
  Success requires the selected verification policy; missing observations mean
  unknown. An API acknowledgement never establishes serving success.
- All background automation requires explicit scoped configuration. External
  configuration owners remain authoritative for their resources. No arbitrary
  shell, file editor, unbounded proxy, or plugin execution API is added.

## Initial supported product contract

First target: Linux/systemd and the existing Compose profile, pinned community
HAProxy/DPA versions, existing enrolled clusters and operator-supplied networking.
Deliver HTTP/HTTPS first, then raw TCP and TLS passthrough within release 1.
HTTP routing initially supports exact hostnames and ordered path prefixes. TCP
listeners have exclusive bind ownership; passthrough supports explicitly defined
SNI routing. Inspection and HTTP rewrites are unavailable on opaque TCP traffic.

Release 1 does not provision machines, claim global failover, implement a mesh,
provide generic UDP balancing, or require a new proxy engine. Supporting a
capability in HAProxy itself does not mean Converge can manage it yet.

### Resources and ownership

| Resource              | Owner/store                                | Required fields and relationships                                                                                    |
| --------------------- | ------------------------------------------ | -------------------------------------------------------------------------------------------------------------------- |
| Application           | Go/PostgreSQL                              | Organization, name, stable ID, protocol, lifecycle, desired revision, later team ownership                           |
| Listener              | Go/PostgreSQL                              | Cluster, permitted bind address/port, HTTP/TCP mode, TLS mode, default behavior; may be shared by HTTP apps          |
| Route                 | Application revision                       | Listener reference, normalized hostnames, path prefixes or supported SNI match, ordered action and destination pools |
| Backend pool          | Application revision                       | Stable identity, algorithm, endpoint source, servers, upstream TLS policy and health-check policy                    |
| Certificate reference | Go metadata + node-local material          | Names, issuer/source, expiry, fingerprint, generation and per-node readiness; no private key                         |
| Application revision  | Go/PostgreSQL                              | Immutable schema-versioned safe intent, parent revision, actor/principal and source                                  |
| Deployment            | Go/PostgreSQL                              | App revision, effective config hashes, target snapshot, per-node results, authority and probe results                |
| DNS binding           | Go reference + Phoenix-owned DNS operation | Organization, application, zone, hostname, traffic endpoint and verified deployment reference                        |
| Discovery binding     | Go/PostgreSQL, release 3                   | Source identity, namespace/service/port selector, destination pool and ownership policy                              |
| Security profile      | Go/PostgreSQL, release 4                   | Immutable ordered policy, required engine capabilities, attached applications/listeners                              |

Use tenant-aware foreign keys and unique constraints. Normalize hostnames and
bind addresses. Reject overlapping/ambiguous routes unless the supported route
precedence makes the winner explicit. A shared listener's default cannot route
unknown hostnames into an unrelated tenant/application. Start with one target
cluster per application; keep the model able to add explicit placements later.

Proposed API namespace: manager `/v1/applications`, `/v1/listeners`,
`/v1/certificates`, with application revisions, deployments, rollback and status
as child resources. Browser routes remain under `/api/haproxy`. Mutations use
idempotency keys and expected resource revisions. Existing backend and runtime
APIs remain compatible, but cannot edit app-owned resources through a second path.

## Release 1: full application delivery

### A0 — Establish the baseline and contracts

- Validate the current maintenance branch/worktree and existing backend rollouts
  with the real two-node lab. Record actual supported versions and constraints.
- Propose an ADR for application/listener ownership, composition, local TLS
  custody, probes and cross-store DNS integration. Separately propose the narrow
  ACME automation contract below. Update the accepted architecture only when
  implementing an agreed decision, not by relabeling this plan accepted.
- Pin official DPA endpoints/contracts for frontend/backend creation, routing,
  certificate storage and asset/reload behavior. Extend provider evidence and
  sanitized fixtures; do not infer certificate/config atomicity from HTTP success.

**Done:** baseline smoke results recorded; schema and resource ownership are
specified; pinned contract tests establish the supported DPA operations.

### A1 — Persist applications and immutable revisions

- Add application/listener/certificate metadata, revision, deployment and binding
  tables to the Go store, with memory and PostgreSQL implementations.
- Add application CRUD, revision creation, status and read-only deployment preview.
  Keep desired, saved-on-node, loaded and externally verified revisions distinct.
- Use existing coarse `haproxy.manage` authority initially. Add full tenant checks
  now; narrower delegation follows in release 2. Audit every lifecycle action.

**Done:** create/read/edit works without changing a node; stale writes and tenant
cross-references fail; deleting an app never deletes a machine or shared listener.

### A2 — Compile and apply effective configurations

- Add a versioned application intent and deterministic compiler. Derive stable
  managed HAProxy section names from resource IDs, not editable display names.
- Map applications onto existing or dedicated managed listeners. Create/update
  backend sections, host/path routing, supported headers/redirects and health
  checks through DPA transactions; preserve unmanaged sections and unknown fields.
- Resolve environment-specific bind addresses and local certificate paths on the
  node. Return a bounded safe projection and content digest to the manager.
- Reject unsupported configurations, overlapping binds and external-owner conflicts.
  Publish agent capabilities so an incompatible target fails before dispatch.
- Initially fence the whole affected effective configuration during a deployment.
  Independent app revisions remain separate even when serialized onto one node.

**Done:** two apps share HTTPS safely; a change to app A preserves app B and all
unmanaged configuration; hostname mismatch takes the explicit default action.

### A3 — Deploy, verify and roll back applications

- Generalize the existing rollout coordinator to immutable application deployment
  bundles and per-node effective artifacts. Permit one-node deployments with a
  visible availability limitation; use explicit availability budgets for larger
  clusters, rather than treating a sequential update as inherently safe.
- Integrate validated whole-node withdrawal/rejoin where a managed entrypoint
  exists. Never assume provider VIP handover or direct/bypass traffic is covered.
- Check syntax, referenced assets, loaded generation, listeners and backend health.
  Add bounded HTTP/TLS probes with Host/SNI against registered traffic endpoints
  from an explicitly selected agent/probe location. Pin destinations, disable
  redirects and enforce local network policy; do not turn topology display URLs
  into arbitrary manager fetch destinations.
- Distinguish node checks from end-to-end entrypoint checks and public reachability.
  A private deployment can succeed under its stated private probe policy; it must
  not be reported as publicly reachable without public evidence.
- Retain durable checkpoints, journaled operations, fencing and unknown outcomes.
  An interrupted commit is reconciled, never blindly repeated.
- Rollback recomposes the selected prior app revision with current revisions of
  other apps. Full-snapshot compensation is permitted only while its complete
  artifact remains exclusively reserved and unchanged; otherwise stop for attention.

**Done:** continuous requests to two apps survive the supported rolling scenario;
a bad health check fails deployment and restores the reserved baseline; manager
restart resumes reconciliation; a stale or unrelated edit is never overwritten.

### A4 — Attach and rotate existing TLS material

- Add certificate-reference configuration using approved node-local paths and
  metadata only. Validate key/certificate match, names, chain and expiry locally.
- Coordinate certificate staging, DPA configuration, activation and persistence
  with a recoverable asset manifest. Exercise runtime activation only if the
  pinned API contract supports persistent recovery as well as in-memory success.
- Keep old material through the rollback window; garbage collect only after
  checking active, pending and rollback references. Expose per-node rotation state.
  Reverting application intent must retain a currently valid compatible certificate;
  do not reinstall expired/revoked material just because an old revision used it.
- Add node-local backup/restore instructions for identities, certificates and full
  snapshots. Manager database recovery alone cannot recreate node private keys.

**Done:** HTTPS serves a trusted certificate for the selected hostname; renewal
or replacement survives reload/restart; invalid material leaves the old listener
serving; rollback cannot reference a prematurely deleted certificate.

### A5 — Deliver the application UI and DNS handoff

Implemented increment (ADR 0039): verified app -> signed 60-second intent ->
ordinary DNS draft, with zone/name permissions, correlation, retries and audit.
The drawer accepts an operator-supplied A/AAAA/CNAME traffic target and opens the
existing DNS review. Customer-side readiness does not verify that supplied target
or public reachability. Automatic DNS cleanup remains separate.

- Add HAProxy Applications list/create/detail views, composed from the included `@ecraela/ui` components.
  Detail sections: Overview, Routes, Backends, TLS, Deployments and Activity.
- The create flow selects a cluster, listener, hostnames, TLS reference, pools,
  checks and verification policy. Show a read-only effect preview, then Deploy.
  Surface partial, unknown, rolled-back and attention states accurately.
- Add optional “Connect DNS” after a verified endpoint exists. A signed,
  tenant-bound request/reference crosses to a Phoenix context that resolves current
  zone/name permissions and prepares an ordinary DNS change. The operator completes
  its existing review/apply workflow in Converge.
- Persist each side independently with correlation/idempotency IDs. DNS failure
  leaves the application running; application rollback does not silently revert
  DNS. Deletion offers explicit DNS cleanup through the same reviewed workflow.
- Expose the created hostname -> DNS operation -> traffic endpoint -> listener ->
  app revision -> backend relationship without presenting it as a distributed
  atomic transaction.

**Done:** an operator publishes a real HTTPS app and separately publishes DNS,
can identify which revision is serving, and can roll back without affecting a
neighboring app. Both light/dark themes, keyboard flow and error states work.

### A6 — Automate certificate issuance and renewal

- First ACME profile: DNS-01 through an explicitly delegated challenge namespace,
  avoiding the requirement to publish application DNS before TLS is ready.
  Existing external certificate management remains supported.
- Node-local issuers generate and retain their own private keys and ACME account
  material. The manager coordinates orders and per-node renewal readiness using
  durable jobs; different nodes may hold different valid certificates for a name.
- A separately credentialed solver is limited to a dedicated validation zone and
  the exact TXT names/values for active orders. Normal DNS-provider credentials
  never reach HAProxy agents. Serialize/coalesce concurrent TXT ownership so one
  order cannot delete another's challenge value.
- The initial delegation is a normal reviewed DNS change. ADR 0040
  authorizes the bounded RFC 2136 challenge profile; renewal cannot bypass
  ordinary zone review by reusing `dns.apply` or general provider credentials.
- Test against an ACME staging environment; add jitter, order/rate budgets,
  expiry alerts, failed-renewal recovery and referenced-asset retention.

**Done:** initial issuance and renewal work before application A/AAAA publication;
the solver cannot modify service records; private keys never enter manager state,
console, telemetry or logs. Without configured delegation, report the prerequisite
and continue supporting local externally managed certificates.

### A7 — Complete protocol and application lifecycle support

- Add exclusive raw TCP listeners and bounded SNI passthrough routing, with
  protocol-specific health checks and verification. Clearly withhold HTTP/WAF
  features from passthrough apps.
- Add explicit disable, delete, hostname change and backend retirement workflows.
  Drain where applicable, remove only owned sections and preserve shared assets.
- Add revision-based weighted application destinations for canary/blue-green
  routing. Separate request/connection weights from promised exact percentages;
  persistence and connection reuse affect observed distribution.

**Release 1 gate:** HTTP/HTTPS/TCP acceptance scenarios pass on real HAProxy,
including TLS renewal, DNS handoff, concurrent app edits, shared listeners,
restart recovery and deletion. Publish supported limits and availability caveats.

## Release 2: team management and operations

### T1 — Resource-scoped authorization

- Retain Phoenix as membership/organization authority. Go owns HAProxy resource
  grants and validates cluster/application IDs in its own database. Reference
  immutable human/team identities; do not couple databases through queries.
- Add explicit roles such as viewer, application operator, cluster operator and
  HAProxy administrator. Separate deploy/runtime/configuration/TLS/access actions.
  Shared listeners require infrastructure permission; app operators receive
  hostname/listener attachments allocated by an authorized infrastructure operator.
- Preserve the owner recovery boundary and existing organization capabilities as
  an upper authority bound. Fine scopes cannot expand a coarse capability.
- Version the identity handoff and update all consumers: Phoenix grant issuer,
  Go claims/session validation (currently capped at two capabilities), browser
  proxy and console. Audit field/count/payload bounds and legacy session behavior.
- Add a signed, monotonically versioned identity/revocation projection from the
  identity authority to Go, with durable delivery/reconciliation. Stop new
  human-authorized side effects when its freshness exceeds 60 seconds; do not
  extend browser sessions to make jobs work. Go resource grants remain local.
- Specify job authority in the ADR: recheck current actor/service grants before
  each new mutating step; reconcile already dispatched writes after revocation.
  Allow only bounded compensation recorded in the accepted deployment authority,
  not an unrestricted new rollback. This deliberately evolves existing rollout
  authorization semantics and needs explicit compatibility tests.
- For standalone installs use a documented local principal/grant authority; never
  require Phoenix for management or confuse installation recovery keys with users.

**Done:** a team can deploy app A but cannot read/edit app B or shared listener
settings; all list/SSE/log/history paths enforce scope; revocation blocks subsequent
dispatch within the stated bound, including queued jobs and reconnects.

### T2 — Automation identities and customer API

- Add Go-owned service principals and scoped, expiring, revocable API credentials
  stored as digests, shown once, with rotation/last-use/audit metadata. Keep
  Converge's existing read-only organization API keys unchanged.
- Publish the supported application API/OpenAPI contract and examples for CI.
  Token mutations honor the same revision, resource and idempotency boundaries.
- Treat installation operator credentials as recovery/bootstrap authority with
  explicit custody. Publish a signed agent image, upgrades/compatibility policy
  and enrollment instructions that work beyond the local lab.

**Done:** CI deploys a revision with authority to one application, cannot issue
credentials or access another app, and loses access after credential revocation.

### T3 — Request investigation, alerts and operational readiness

- ADR 0045 implements bounded private VictoriaLogs request ingestion/search,
  compiler-owned app/configuration attribution, recorded response/rate/latency
  views, collection-loss status and incident investigation. Configuration/audit
  remain in PostgreSQL and node metrics in VictoriaMetrics. Broader content
  search, sampling controls, security events and measured capacity remain open.
- Capture stable app/route/revision IDs plus safe response/timing metadata. Default
  collection excludes bodies, cookies, authorization headers and query strings;
  GeoIP enrichment is optional with a documented database/update source.
- Derive application identity from trusted compiler-generated transaction metadata,
  not client-supplied headers. Shared frontend counters cannot be attributed to
  each app in full; use correctly scoped counters/events and expose any loss.
- Add application-level status/error/latency views and fixed bounded log queries.
  Name latency statistics honestly; averages do not become p95/p99. Record sampling
  and loss so searches are not presented as complete when they are not.
- ADR 0044 implements fixed durable application health, expiry, unavailable
  telemetry and deployment alerts with grouping, cooldowns and recovery.
  Destination configuration is installation-owned and delivery is opt-in.
  Add custom policies and further delivery adapters later; do not send external
  messages while implementing a demo.
- Add manager database/node-state backup and restore drills, agent upgrade/rollback,
  retention/cleanup, resource limits and soak/load tests. Publish measured capacities
  for the supported shape; the local entrypoint/host is not proof of production HA.
  The local checkpoint/restore and bounded load/failure portion is implemented in
  [the hardening suite](haproxy-hardening.md); production backup operations,
  cross-version recovery and supported upgrades still require qualification.

**Release 2 gate:** two teams operate separate apps on shared infrastructure;
CI deployment, revocation, scoped investigation, alerts and restoration work.
Document an actual support/upgrade process before promising enterprise support.

## Release 3: discovery

### D1 — Discovery contract and reconciliation

- Add source/binding/observed-snapshot/reconciliation records, workload identities,
  rate budgets and per-source status to Go. Run discovery as a separately bounded
  role with an outbound site connector for private APIs; no customer Kubernetes
  API exposure or browser-uploaded general admin kubeconfig is required.
- Bind a selected source to one app pool under explicit continuous-reconciliation
  authority. Static and discovered ownership cannot both edit the same endpoint set.
  Manual emergency overrides have owner, expiry and visible suspension semantics.
- Persist desired/source generations and coalesce updates. Use the application
  compiler and node mutation serialization; do not create a second file writer.
- Distinguish complete authoritative empty results from failed/partial collection.
  On source outage retain the last known endpoint set, mark it stale and continue
  local health checks. An explicit complete empty set removes routing candidates
  and yields unavailable/503, rather than silently restoring deleted workloads.
- Extend the current non-empty backend validation intentionally: allow an empty
  discovery-owned pool with defined unavailable behavior. Never route its traffic
  into another application's fallback pool.

### D2 — Kubernetes first

- Watch namespace-scoped Services and EndpointSlices using least-privilege RBAC.
  Start with explicit Service and named-port bindings, IPv4/IPv6, and endpoints
  reachable from the selected HAProxy nodes. NodePort is a separate supported mode.
- Implement list/watch resync, expired resource versions, duplicates, out-of-order
  events and bounded debounce. Handle `ready`, `serving`, `terminating` and absent
  conditions according to the pinned Kubernetes contract; explicitly handle
  `publishNotReadyAddresses`, rather than treating every ready flag as health proof.
- Keep app exposure explicit initially; observing a Service never automatically
  publishes it. Ingress/Gateway resources, IPAM and LoadBalancer status writes
  require subsequent pinned integration contracts and explicit authority.
- Distinguish external HAProxy managed by Converge from HAProxy generated by an
  ingress controller. Integrate with the latter's Kubernetes configuration owner;
  never edit its generated files through DPA.

**Done:** scaling, replacement, terminating pods, full endpoint removal, API
outage, watch restart and RBAC denial produce the defined routing behavior;
no unrelated namespaces/services become visible or exposed.

### D3 — Consul and multi-cluster placements

- Add a pinned Consul adapter with ACL tokens, selected service/datacenter scopes,
  health filtering and blocking-query recovery. Namespace/partition support is a
  separately tested capability; do not imply Enterprise parity from basic support.
- Extend applications to explicit cluster placements and weighted destination
  groups. Add network reachability checks and staged cross-cluster deployments.
  Keep DNS traffic policy, proxy routing and entrypoint failover distinct.

**Release 3 gate:** explicitly selected Kubernetes and Consul services remain in
sync through churn/outages; application canaries and supported multi-cluster
placements preserve ownership, health and audit. Publish measured convergence
and reload budgets, not an untested universal discovery latency promise.

## Release 4: security

### S1 — Policy model and local enforcement

- Add versioned ordered security profiles with typed signals/actions, app/listener
  attachments and per-node required capabilities. Compile to supported HAProxy
  ACLs, maps, counters and bounded external-inspection configuration.
- First policies: IP allow/deny, method/path/header predicates, explicit request
  and connection rate limits, supported deny/allow actions and application limits.
  Define trusted-proxy/client-IP handling before any client-based policy.
- Define whether limits apply per node, key or deployment. Peer replication is
  not assumed to be an exact global distributed counter. Global limits require
  a separate aggregation design, fault policy and measured accuracy.
- Learning mode records the ordered would-match/would-act decision while leaving
  traffic unchanged; enforcement and learning use the same evaluated policy.
  Availability is local: management outage must not disable the loaded policy.
- Build a simple policy list/editor before a visual graph. Add a visual editor
  only over the same typed model after conflict/precedence semantics are stable.

### S2 — WAF integration and lifecycle

- Evaluate a pinned Coraza HAProxy SPOA integration and OWASP CRS release on a
  disposable lab. Record provenance, compatibility and resource/latency budgets.
  Keep the adapter replaceable; do not claim Enterprise WAF equivalence.
- Add signed/versioned rule bundles, rule exceptions and per-app inspection modes.
  Stage rule assets before configuration activation with the same recoverable
  deployment/rollback manifest as certificates and other node-local assets.
- Declare maximum body sizes, supported encodings/content types, truncated-body
  behavior, inspection timeouts and explicit fail-open/fail-closed policy. Require
  TLS termination at the inspection point; opaque passthrough is unsupported.
- Test malicious and legitimate traffic, exclusions, malformed input, engine
  crash/overload, body limits and bundle rollback. Measure false positives and
  resource costs against a defined corpus, not a dashboard count.

### S3 — Security investigation and controlled response

- Add bounded security events to the log pipeline with app/revision/policy/rule
  identity, observed signal, learning/enforced action and reason code. Do not
  retain raw payloads or credentials to make a chart richer.
- Provide learning/enforcement comparisons, top triggered rules and request/event
  investigation. Show sampling and loss; absent telemetry is not zero attacks.
- Add explainable brute-force/rate heuristics before advanced bot scoring.
  Challenges, reputation feeds and automated response are later separately tested
  engines with expiry, error budgets and false-positive recovery. A CAPTCHA is
  not evidence of equivalent bot classification.

**Release 4 gate:** an operator attaches one profile, observes would-block events,
enforces it on a canary, investigates decisions and restores a previous revision.
WAF/manager failure obeys the selected local policy, and legitimate traffic stays
within the published false-positive and performance acceptance thresholds.

## Implementation map

All paths below are relative to the repository root. Proposed new files/packages
should follow existing conventions; names describe ownership, not a mandate to
split services prematurely.

| Area              | Existing extension points                                                                   | Planned additions                                                                                               |
| ----------------- | ------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| Domain            | `internal/domain/{configuration,rollout,domain}.go`                                         | Application intent/revisions, listeners, certificates, deployments, grants, discovery and security types        |
| Persistence       | `internal/store/`                                                                           | Tenant-aware schemas, immutable revisions, durable workers and memory/PostgreSQL contract parity                |
| Node compiler/DPA | `internal/dataplane/client.go`                                                              | Allowlisted configuration projection, ownership/composition, TLS/asset staging and transactional recovery       |
| Agent protocol    | `proto/control/v1/control.proto`, `internal/agent/control.go`, `internal/controlwire/`      | Capability negotiation, versioned bundles, status and bounded verification results; regenerate pinned Go output |
| Product API       | `internal/server/`                                                                          | Application endpoints, previews, deploy/rollback/status, scoped APIs and query enforcement                      |
| Console           | `web/src/haproxy*.tsx`, `web/src/haproxy-api.ts`                                            | Application pages, scoped actions, discovery and security views using `web/ui`                                  |
| Identity          | Go local auth, browser grants, server/store code; optional Converge identity handoff        | Versioned grant contract, resource grants, revocation projection and service principals                         |
| DNS integration   | Go DNS binding metadata; optional Converge DNS contexts in the separate Converge repository | Narrow signed handoff, correlation IDs and ordinary reviewed DNS operations                                     |
| Operations        | Deploy profiles, `scripts/`, `Makefile`, `.github/workflows/acceptance.yml`                 | Application/TLS/discovery/security smoke suites, compatibility and recovery checks                              |

## Dependency order and first implementation batch

```text
A0 -> A1 -> A2 -> A3
            |     |
            +-> A4 -> A5 -> A6 -> A7 -> release 1
                                      |
                      T1 -> T2 -> T3 -> release 2
                                      |
                      D1 -> D2 -> D3 -> release 3
                                      |
                      S1 -> S2 -> S3 -> release 4
```

First batch is deliberately small enough to review as separate changes:

1. Baseline validation plus the proposed application-delivery ADR and pinned DPA
   contract evidence. No unrelated cleanup of the existing working tree.
2. Application/listener/revision schema, authorization and CRUD API, with no node
   writes. Define the shared-listener conflict cases in tests.
3. Deterministic HTTP application compilation and local DPA transactions, tested
   against a real proxy with two apps and unmanaged configuration preserved.
4. Durable application deployment/rollback with client-path probes and recovery.
5. Node-local TLS references and Applications UI, producing the first usable
   HTTPS publication flow before certificate automation and discovery.

Then finish DNS handoff, ACME and protocol/lifecycle coverage for release 1.
Do not implement advanced security or new proxy engines during this batch.

## Verification and release discipline

For material implementation changes, follow the repository's `make format`,
`make check`, `make test`, `make build` requirements. Extend the manager's race,
PostgreSQL, real-proxy and UI checks at the work package that changes behavior.
Run contract tests for both new schema behavior and migrations of existing data.

Required regression scenarios across releases:

- Cross-tenant and cross-resource reads/writes, list pagination, SSE, history,
  probes, log queries and credential lifecycle.
- Multiple apps on one listener, concurrent changes, hostname conflict, external
  edits, bounded worker retries and incompatible/older agents.
- Disconnect before/after commit, manager restart, duplicate requests, failed
  health verification, unknown results and incomplete compensation.
- TLS/auxiliary asset activation, node restart, rollback retention and backup
  restore; no private material in the control plane or captured output.
- DNS permissions narrowed between preparation and apply; deployment/DNS partial
  completion remains recoverable with independent truthful statuses.
- Collection overload/storage outage must not starve commands or serving traffic.
  A private-node probe does not establish public reachability.

The primary release-1 demo is two applications on a real two-node HAProxy fleet:
publish trusted HTTPS, keep continuous traffic on both apps, change app A, reject
a bad revision, recover after controller restart, rotate its certificate, publish
DNS through the normal workflow, and remove A while B continues serving.

## Decisions and evidence to close during implementation

- Proposed defaults here: one cluster per app initially; HTTP host/path routing;
  local certificate custody; delegated DNS-01; Kubernetes before Consul; Go-owned
  resource grants; VictoriaLogs as the first log-store candidate. Change these
  through the relevant design spike/ADR if measured constraints require it.
- Production deployment limits, capacity targets, WAF acceptance thresholds,
  retention/storage budgets, signed image distribution and support commitments
  need measured release criteria. Set numerical budgets before each benchmark,
  record hardware/configuration and publish results rather than inventing capacity.
- Managed provisioning, general VIP/IPAM, GSLB/automatic DNS failover, generic UDP,
  full Gateway API implementation, advanced bot intelligence and global edge
  infrastructure remain separate follow-on work. DNS adapter support alone does
  not establish those capabilities.

Official references checked on 2026-10-25 (pin exact releases/specifications before
implementation; these links establish capabilities, not production validation):

- [DPA certificate management](https://www.haproxy.com/documentation/haproxy-data-plane-api/tutorials/certificates/)
- [DPA configuration reference](https://www.haproxy.com/documentation/haproxy-data-plane-api/reference/configuration-file/)
- [ACME challenge types and delegation](https://letsencrypt.org/docs/challenge-types/)
- [Kubernetes EndpointSlice behavior](https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/)
- [EndpointSlice API condition semantics](https://kubernetes.io/docs/reference/kubernetes-api/discovery/endpoint-slice-v1/)
- [Coraza HAProxy connector](https://www.coraza.io/connectors/)
- [HAProxy stick tables and replication](https://www.haproxy.com/documentation/haproxy-configuration-tutorials/proxying-essentials/custom-rules/stick-tables/)
- [Consul health API](https://developer.hashicorp.com/consul/api-docs/health)
- [Consul blocking queries](https://developer.hashicorp.com/consul/api-docs/features/blocking)
- [VictoriaLogs deployment, retention and multitenancy](https://docs.victoriametrics.com/victorialogs/)

### Outbound Converge connector (pilot)

Implemented in ADR 0049: Go relay, tenant-bound linking drawers, optional local read-only management policy, bounded live/control transport, durable credentials, revocation and independent local login. The two-node scenario tests relay/manager restart and HTTP/HTTPS continuity. Multiple managers per organization and multi-replica relay routing remain future work.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
