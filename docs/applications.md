# Applications and TLS

Create HTTP applications on shared listeners, verify deployments and configure HTTPS.

Application [traffic rules](traffic-rules.md) add ordered IP/CIDR, country and
ASN access controls, observe/enforce modes and recorded match evidence. They
use the same editing drawer, application versions and verified rolling updates.

## HTTP applications

Select a cluster in the console and use **Applications → Create application**.
Creation, editing and version history open in side drawers with scrolling content
and fixed actions; persistent backend editing uses the same pattern.
Enter an exact hostname, path prefix, balancing strategy, backend servers and
HTTP health-check path, then **Save and deploy**. The existing `haproxy.manage`,
shared-login/MFA and local agent configuration policy apply directly.
**Version history → Restore application version** creates a new revision and
rolls it out while preserving the other applications' current settings.

This HTTP profile uses the cluster's declared existing frontend and managed
entrypoint on 2–12 enrolled nodes. Upgrade all agents to the application-capable
version. Hostnames match exactly, ignoring case and an optional port. The longest
literal path prefix wins, and paths are forwarded unchanged. Duplicate routes
are rejected. Existing frontend binds, rules and fallback routing are preserved;
unmatched traffic uses that existing fallback. A single application has up to 16
backend servers, with HAProxy HTTP GET checks every second, two successes/failures
for up/down and the default 2xx/3xx success range. New backends are compiled into
UUID-derived managed sections rather than changing an unrelated pool.

The initial profile requires identical unmanaged backend projections on the
cluster's nodes; node-specific non-editor settings stay local. Application
metadata and immutable versions live in Go/PostgreSQL; raw configuration, DPA
credentials and full rollback artifacts stay on nodes. Edits, drift checks,
unknown outcomes and drain/rejoin recovery use the existing operation protocol.
Generic node controls cannot overwrite application-owned configuration.
After a partial deployment stops, recover its maintenance reservation first;
a new explicit application deployment can reconcile that application's known
partial versions without adopting or changing another application's state.

Bounds are 16 applications and 256 total application revisions per cluster,
with at most 64 revisions per application. History is not silently pruned.

From the repository root:

```sh
python3 scripts/applications.py
curl -H 'Host: apps.demo.test' http://localhost:18081/shop/items
curl -H 'Host: apps.demo.test' http://localhost:18081/shop/admin/v2/users
```

The smoke uses **Store demo** and **Admin demo** on the existing `public`
listener. It verifies both proxies serve each app, longest-prefix routing,
continuous neighbouring-app traffic, manager restart, an app rollback that keeps
a newer neighbouring revision, failed HTTP health recovery and preservation of
unmanaged backend settings. The two demo applications remain available afterward.
DNS publication is separate; the local `Host` header is sufficient for this lab.

### Application details and entrypoint checks

Choose **View application** from an application's menu to inspect routes,
per-node matching revisions, backend health, HTTP/HTTPS results, certificates
and deployment history. A matching version describes observed configuration
content; identical revisions cannot be distinguished by the content digest.
The list separates **Last deployed** from **Entrypoint checks**.

Enable read-only checks with an agent-local JSON file and
`--probe-policy-file /etc/haproxy/probes.json`. Existing local configuration
discovery must be configured; write permission is not required to run probes.

```json
{
  "checks": [
    {
      "name": "store-https",
      "listener": "public",
      "hostname": "apps.example.com",
      "path_prefix": "/shop",
      "path": "/shop/health",
      "scheme": "https",
      "address": "10.20.0.10",
      "port": 443,
      "expected_status": 200
    }
  ]
}
```

`address` is the customer's real entrypoint as reachable from that agent, not
necessarily its own HAProxy node. The policy binds an exact frontend, hostname
and route prefix; `path` must stay within that route and cannot enter a more
specific neighboring application. Use a frontend-visible path, which can differ
from the backend health path. Optional `response_header` and `response_value`
check application-specific responses; expectations and response content stay
local. Add an HTTP check separately if HTTP must also serve traffic.

HTTPS verifies chain, expiry and hostname using system trust. For private PKI,
add `"ca_file": "/etc/haproxy/probe-ca.pem"`; the file stays local. Destinations
resolve once per control session and are pinned; restart the agent after policy,
trust or address changes. Redirects and environment proxies are not followed.

Checks run every five seconds, independently of collection and management, with
bounded concurrency, timeouts and response sizes. The outbound control stream
reports only safe results, duration, app digest and public certificate metadata.
`GET /v1/clusters/{cluster}/applications/status` uses `haproxy.read` through the
standalone operator API or shared browser login. Missing/disconnected/stale data
is **Unknown**; a deployment success alone never establishes reachability.
Checks prove access from their listed locations, not public internet access or
that every upstream node received a request. They observe entrypoint delivery;
direct node rollout verification uses a separate local scope below.

The pair setup creates a local probe policy for both demo apps and protocols.
After `python3 scripts/applications.py` and `python3 scripts/listeners.py`, run
`python3 scripts/probes.py`. It stops only Store's backend, verifies Admin remains
reachable, restores Store, tests loss of a probe agent and restores it. No
application revisions are written by these checks.

### Application-aware rollout verification

Upgrade every agent to application control version 3 or 4. In each agent's local
probe policy, add checks with `"scope": "node"` whose `address`/`port` point
directly at that agent's own HAProxy instance. Retain separate entrypoint checks
for the application reachability view. A shared VIP/entrypoint must never be
marked node scope. For example, a local HTTPS check can use:

```json
{
  "name": "store-node-https",
  "scope": "node",
  "listener": "public",
  "hostname": "apps.example.com",
  "path_prefix": "/shop",
  "path": "/shop/health",
  "scheme": "https",
  "address": "127.0.0.1",
  "port": 443,
  "expected_status": 200,
  "response_header": "X-Application",
  "response_value": "store"
}
```

Use the node's reachable private/container address instead of loopback when the
agent has its own network namespace. Add `ca_file` for private trust and a
separate HTTP check if required. Restart the agent after local policy/trust
changes. The manager receives safe grants/hashes, never these destinations,
response assertions or roots.

In the Create/Edit application drawer, select **Verify HTTP** and/or **Verify
HTTPS** under Rollout request verification. The standalone deployment API uses
`"verification": {"http": true, "https": true}` in the app spec. Every selected
protocol must have a matching node-scope grant on every target. This profile
requires the managed entrypoint withdrawal/drain binding; it does not infer a
provider VIP adapter. All active verified apps in the effective configuration
are checked, including unchanged neighbors sharing the listener.

The coordinator requires two distinct fresh passing checks before rejoin and
two after rejoin, bound to the exact application/policy/artifact/generation.
HTTP, response assertion and HTTPS trust/expiry failures trigger rollback.
Missing evidence waits at most thirty seconds per phase. Restored baseline apps
must also pass. If recovery cannot be verified, the node stays withdrawn under
**Recovery needs attention**; inspect its local checks and version history before
explicit recovery. Deployment and rollback results remain visible in the
console's rolling configuration panel. Direct checks do not prove public access.
HAProxy permissions/MFA apply
directly with no approval; DNS publication/cleanup keeps ordinary review.

To gate updates on requests through the shared entrypoint as well, upgrade every
agent to application control version 4 and enable **Also verify through the
shared entrypoint after rejoin** in the application drawer. The standalone API
accepts `"verification": {"http": true, "https": true, "entrypoint": true}`.
Every selected protocol then requires both a node grant and an `entrypoint`
grant on every agent. Existing entrypoint checks without a scope use that scope
by default. Point their local destination at the actual entrypoint; keep node
destinations direct. Paths may differ, but each must stay within the exact app's
route. Check representative requests for both locations.

Two fresh entrypoint passes are required after rejoin before moving to the next
node, alongside the fresh direct checks. Entrypoint response/TLS failure,
timeout and changed local policy have separate rollout errors. Compensation
withdraws a rejoined node, restores its baseline and directly verifies it before
and after rejoin. Once all changed nodes are restored, both paths must pass from
every location. A shared request may hit a peer still awaiting rollback, so
shared checks must not block compensation between individual restorations.
Failed direct recovery keeps the node withdrawn. An external shared-path failure
after all baselines return reports attention while directly verified nodes keep
serving. These probes prove access from the
named agent locations, not public reachability or which upstream received a
shared request. See [ADR 0043](adr/0043-haproxy-entrypoint-rollout-verification.md).

For the additional entrypoint gate demo, keep both apps and trusted HTTPS active:

```bash
python3 scripts/entrypoint_verification.py --prepare
python3 scripts/entrypoint_verification.py
```

Preparation waits for active updates, upgrades management/agents and starts an
opt-in fault backend. It changes only the four known synthetic direct-check
paths in each demo agent policy to `/node-check` under their existing prefixes;
custom local profiles are rejected rather than overwritten. Preparation adds
several entrypoint checks to exercise round-robin selection. Use a CA lifetime of
at least one hour; the smoke requires fifteen minutes left on the loaded leaves
so short renewal fixtures cannot expire while application updates reserve the
cluster. The failure backend
passes those paths and HAProxy health checks but returns 503 for `/shop/items`.
The smoke enables entrypoint policy on both apps, then separately tests HTTP and
HTTPS failures through the shared entrypoint while direct node requests pass.
It verifies rollback, restored node/entrypoint evidence, unchanged app B and
shared TLS under continuous app B HTTP/trusted-HTTPS traffic. The fixture does
not simulate public DNS or every possible VIP failure.

The local telemetry service reserves less cache and caps its Go memory budget
below the container limit, avoiding out-of-memory exits during demo replay and
chart queries. Its existing data volume and history are retained.

For an existing pair, generate missing local profiles without replacing custom
files, then rebuild/recreate only management services while no rollout is active:

```bash
PYTHONPATH=scripts python3 -c 'from pair import selected_state; from probes import prepare_assets; prepare_assets(selected_state())'
PYTHONPATH=scripts python3 -c 'from pair import compose; compose("build", "manager"); compose("up", "-d", "--no-deps", "manager", "ingest", "agent-a", "agent-b", "agent-entrypoint")'
python3 scripts/application_verification.py
```

The two apps and trusted HTTPS must already be configured
(`python3 scripts/applications.py`, `python3 scripts/listeners.py`, optionally
`python3 scripts/acme.py up`). New
pair setups create separate `probe-policy-a.json`/`probe-policy-b.json` with both
entrypoint and direct checks. The smoke enables HTTP/HTTPS policy on both apps,
then deploys a transport-healthy but incorrect app A backend separately for
HTTP and HTTPS. Both must automatically roll back before another node changes.
App B is continuously requested over HTTP and trusted HTTPS. App A is restored,
shared listeners and loaded TLS retained, and safe rollout evidence saved in the
private demo state. Invalid certificate trust is also covered by real TLS probe
unit tests; this smoke's HTTPS fault is an application-response mismatch.

### HTTPS listeners and certificate rotation

The **HTTPS listeners** section adds or edits HTTPS in a drawer and exposes
immutable listener history. It applies directly with `haproxy.manage`, existing
MFA and node-local configuration policy. There is no review or approval stage.
Customers own/provision HAProxy nodes, application servers and traffic networking;
the hosted service supplies management. Self-hosted installations use the same
Go code and can use the operator API independently of Converge.

On each node, install immutable PEM files containing the leaf/chain and matching
private key. Grant them to the agent using `--tls-policy-file /etc/haproxy/tls.json`:

```json
{
  "bindings": [{ "name": "public", "address": "*", "port": 8443 }],
  "certificates": [
    {
      "reference": "site-v1",
      "path": "/var/lib/haproxy/certificates/site-v1.pem"
    },
    {
      "reference": "site-v2",
      "path": "/var/lib/haproxy/certificates/site-v2.pem"
    }
  ]
}
```

Enable configuration control and give the agent and HAProxy read access to those
same absolute paths. Keep the policy and key files private. For a separate
container network namespace, add a locally configured `"probe_host": "haproxy-a"`
(the node's resolvable name/IP); otherwise the agent probes the local bind address.
This host never comes from a manager request. A policy change requires restarting
the agent. Only public metadata and loaded leaf pin observations are reported.

This profile adds HTTPS to the cluster's existing HTTP frontend, preserving HTTP
binds. Certificates must cover its application hostnames, remain valid for more
than one minute, and have the same alias/leaf identity on all 2–12 target nodes.
TLS 1.2+, strict SNI and matching Host/path rules are enforced. Unknown HTTPS
routes return 404. Publish DNS and open the traffic port in customer networking
separately. Agent pin verification does not prove public trust or reachability.

Rotate by installing a new immutable file/alias on every node, then selecting it
in the drawer. Keep old files while rollback is needed. Restoring a listener
version recomposes current application/backend settings and rechecks the old
certificate's validity. Do not replace bytes behind an existing alias/path.
For node-local ACME issuance and renewal with independent per-node certificates,
see [Automatic TLS with DNS-01](#automatic-tls-with-dns-01) below. General file
distribution and certificate garbage collection remain outside this profile.

```sh
python3 scripts/pair.py up
python3 scripts/applications.py
python3 scripts/listeners.py
```

The TLS smoke uses a private local CA, two applications on `public`, trusted SNI
requests, invalid-certificate rejection, live rotation, manager/node restarts,
rollback and HTTPS failover. HTTP stays on `http://localhost:18081`; HTTPS enters the TCP pass-through
on `https://apps.demo.test:18444`. Both ports use the same two upstream identities,
so whole-node withdrawal/draining covers both. Existing labs are migrated through
the local Data Plane API without overwriting their persisted configuration.
The original single-node demo on 18080 is separate.

Pair application containers use a separate image so rebuilding management does
not replace them or change their IPs. Named application servers resolve DNS when
HAProxy loads configuration; continuous service discovery is separate roadmap
work. After changing the demo backend image itself, refresh those resolutions
before running health-gated operations.

For a manual request, substitute your private pair state directory for `<pair>`:

```sh
curl --cacert '<pair>/tls/ca.crt' --resolve apps.demo.test:18444:127.0.0.1 \
  https://apps.demo.test:18444/shop/items
```

API: `GET /v1/clusters/{cluster}/listeners` reads up to 64 immutable versions;
`POST` at that path deploys `listeners`, `request_id`, `expected_revision`, current
`nodes` preconditions and the managed `entrypoint` precondition. A listener has
`name`, granted `address`/`port`, `certificate_ref` and `certificate_sha256`.
`POST /v1/clusters/{cluster}/listeners/versions/{revision}/rollback` creates a new
deployment from an earlier successful version. An empty listener array disables
managed HTTPS while preserving HTTP applications. Private keys and paths are
never accepted by these endpoints.

API (same independent Go service in cloud and self-hosted deployments):

- `GET /v1/clusters/{cluster}/applications` (`haproxy.read`) returns the bounded
  immutable revision history with current rollout outcomes.
- `POST /v1/clusters/{cluster}/applications` (`haproxy.manage`) creates and deploys
  an application. Supply `request_id`, `expected_revision: 0`, `spec`, current
  `nodes` preconditions and the exact `entrypoint` preconditions/binding used by
  rolling updates. `spec` contains `name`, `listener`, `hostname`, `path_prefix`,
  `algorithm`, `servers` and `health_path`; the manager assigns its stable ID.
- `POST /v1/clusters/{cluster}/applications/{application}/deploy` requires the
  latest application `expected_revision`; it creates the next immutable revision.
- `POST /v1/clusters/{cluster}/applications/{application}/versions/{revision}/rollback`
  uses the selected stored app intent and the current preconditions. It recomposes
  current neighbours and records a new revision with `rollback_of`.

Browser mutations also require the manager's CSRF token and exact Origin. Identical
request retries return the same deployment. Schema migration 10 preserves existing
node history and adds tenant-aware app/revision/rollout relationships.

### Automatic TLS with DNS-01

[ADR 0040](adr/0040-haproxy-automatic-dns01-tls.md) adds node-local ACME
issuance and renewal using a dedicated RFC 2136 validation zone. Both hosted and
standalone managers use the same code, database and mTLS endpoints. The first
profile supports 2–12 nodes and eight exact hostnames on the cluster frontend;
your CA's quotas may be lower. Each generation issues one certificate per node.

1. Configure an authoritative validation zone you operate. Issue each node a
   separate HMAC-SHA256 TSIG key with exact-name, TXT-only server ACLs, for example
   `grant node-a.validation.example.net name app.validation.example.net TXT;`.
   Do not grant changes to the application zone or all record types.
2. Grant the existing HTTPS binding in the agent's local TLS policy. An initially
   empty `certificates: []` is supported for automatic issuance. Keep HTTP app
   routing and HTTP entrypoint probes working before the first certificate.
3. Create the local ACME policy and TSIG secret file with private permissions.
   The following policy is illustrative; replace the directory with your CA's
   staging URL and explicitly agree to that CA's terms in your local process.

```json
{
  "certificate_dir": "/var/lib/haproxy/certificates/acme",
  "profiles": [
    {
      "issuer": "our-staging-ca",
      "listener": "public",
      "environment": "staging",
      "directory_url": "https://acme-staging-v02.api.letsencrypt.org/directory",
      "terms_agreed": true,
      "dns_server": "192.0.2.53:53",
      "resolver": "192.0.2.54:53",
      "validation_zone": "validation.example.net",
      "key_name": "node-a.validation.example.net",
      "key_file": "/etc/haproxy-agent/dns01.key",
      "challenges": [
        {
          "hostname": "app.example.com",
          "target": "app.validation.example.net"
        }
      ]
    }
  ]
}
```

4. Mount the certificate directory at the **same absolute path** in the agent
   and local HAProxy/DPA. Agent: write access. HAProxy: read access through its
   dedicated group. Use a setgid directory (for example `2750` owned by the agent
   and HAProxy group) so atomically created `0640` PEMs inherit that group. With
   dropped container capabilities, the initialization process must also belong
   to that group for the setgid bit to stick. Persist the agent state and use a
   separate volume per node; never share private keys across the pair.
5. Start the agent with `--acme-policy-file /etc/haproxy-agent/acme.json`, the
   existing TLS policy, and configuration control. Optional `trust_file` is an
   absolute path to public roots for private PKI; public CAs use system trust.
   The resolver must be able to observe the public CNAME/TXT chain in production.
6. In **Automatic TLS**, inspect the delegation instructions and publish
   `_acme-challenge.app.example.com CNAME app.validation.example.net` through
   your DNS provider's normal change process. With the optional Converge
   integration, this can be prepared as a DNS draft and follows its normal
   review, approval, apply and verification. A draft alone cannot validate an
   order. Enabling HAProxy TLS requires management permission, without HAProxy
   review or approval. Converge management actions also require its existing MFA.
7. Enable automatic TLS. Agents register local accounts, persist local keys and
   resume orders. Once every node has a usable certificate, the manager starts
   the normal rolling drain/update/health/rejoin sequence. Observe progress,
   failures, retry dates, expiry and generation history in the drawer. **Renew
   now** uses the same path. **Disable renewal** stops new issuance and scheduled
   activation; a rollout already started completes safely. Existing PEMs remain.

Renewal runs at 60–69% of the shortest certificate lifetime, with bounded retries
and CA Retry-After. Temporary offline nodes defer renewal. Changed node roster,
hostnames, topology, local grants or a newer manual listener choice requires
explicit setup again. Revoked identities and disabled plans lose issuance
authority. Restart recovery reuses immutable keys/orders and cleans only owned
TXT values. No arbitrary command, path or CA URL comes from the manager.

Retain immutable certificate files needed for active/rollback versions. The agent
bounds storage at 128 issued assets and 512 private journal/account files; it
pauses rather than deleting referenced keys. Plan summaries retain 20 generations;
listener history lists the latest 64 revisions, with monotonically increasing
numbers. Existing configuration artifact limits also apply. Archival automation,
wildcard/EAB/ARI and provider API solvers are
subsequent work. Test with the CA's staging service before production; this local
lab does not prove public CA quota, trust or internet reachability.

For the opt-in two-node demo (after `python3 scripts/pair.py up` and the application/TLS
fixtures), run from the repository root:

```sh
python3 scripts/acme.py up
python3 scripts/acme.py smoke
```

The lab builds pinned Pebble 2.10.1 and uses pinned BIND 9.20.30. It has a private
pre-existing CNAME delegation fixture and verifies actual DNS-01 challenges;
there is no validation bypass or public DNS/provider write. The console can
separately prepare the CNAME's normal DNS draft. Test trust roots are confined to
the lab and are not installed in your OS/browser. Its default certificate
lifetime is one day; `ACME_DEMO_LIFETIME_SECONDS=600 python3 scripts/acme.py up` on initial
setup accelerates renewal checks. Pebble is a disposable test CA: restarting it
loses CA/account state and creates new trust roots; use pause/unpause to test CA
outages without changing trust. Existing demo nodes, state and keys are retained.



### Application disable, re-enable, delete and restore

[ADR 0041](adr/0041-haproxy-application-lifecycle.md) adds lifecycle
actions to the application menu and detail drawer. They use `haproxy.manage`,
the existing management MFA and local agent policy, without HAProxy approvals.
Every action creates an immutable revision and a sequential
withdraw/drain/update/health/rejoin rollout with existing compensation behavior.

- **Disable application** keeps its hostname/path reserved and returns 503 from
  an empty owned backend. Original endpoints remain in safe intent for
  re-enabling. More specific active routes continue serving; a disabled nested
  route does not fall through to its parent. Existing node connections drain.
- **Re-enable application** restores those endpoints and checks health before
  rejoining each node. Editing a disabled app preserves its disabled state.
- **Delete application** removes only its owned routes/backend. Shared listener
  binds, TLS references/material, other apps and unmanaged settings stay intact.
  Removed routes may match an existing broader route. Deletion retains history.
- Select **Show deleted applications**, then **Restore application** to deploy
  its saved settings again. A conflicting route fails rather than displacing
  its new owner. A stale editor cannot resurrect a deleted app. Deleted apps do
  not consume active application slots; the existing 64 revisions per app and
  256 cluster-wide revision history bounds still apply.

The standalone operator API uses
`POST /v1/clusters/{cluster}/applications/{application}/lifecycle` with `action`
(`disable`, `enable`, `delete`, `restore`), `request_id`, `expected_revision`,
`nodes` and `entrypoint` preconditions matching application deployment. It does
not accept endpoint settings. Every target must advertise application control
version 2, 3 or 4; direct request verification needs version 3 or 4 and entrypoint
gates need version 4. Upgrade the agents
before using lifecycle actions. Ordinary version 1
application deployment remains supported. Disabled/deleted observations expire
to unknown when the agents disconnect. They are not classified as failed apps.

DNS is preserved. The standalone console shows cleanup instructions for your
DNS provider. With Converge, **Review DNS cleanup** opens the ordinary zone draft
editor/review. Keep records for a hostname shared with another app. Perform any
removal explicitly through the DNS provider's normal change process. Application
deletion does not automatically prepare cleanup drafts or delete DNS-01
delegation/certificate files. A real hostname change can pause ACME authority; a temporary app rollout
defers scheduled renewal until its node projections converge.

After the two-app HTTPS demo is ready, run:

```sh
python3 scripts/application_lifecycle.py
```

It disables, re-enables, deletes and restores only Store demo, verifying app A's
503/200/404/200 responses on trusted HTTPS while continuously checking
Admin demo on both protocols. It verifies per-node configuration, shared listener
binds and loaded TLS, then leaves both apps enabled. Plain HTTP deliberately
retains the demo's unmanaged default backend: after route deletion it returns
200 through that fallback, while the HTTPS route guard returns 404. Deleting a
managed app does not alter existing fallback routing. To test the same sequence
using the console, start
`python3 scripts/application_lifecycle.py --watch-console`
and perform those four actions in order. The observer can resume with re-enable
or restore if the demo is already disabled or deleted.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
