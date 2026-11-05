# Metrics incidents and request investigation

Inspect node metrics, application traffic and bounded request telemetry.

## Operational incidents

The manager evaluates fixed incident rules independently of browser sessions.
The console's **Incidents** section shows active problems and a retained timeline;
select an incident to open its details drawer. `haproxy.read` is sufficient.
HAProxy changes still require permissions and local policy, with no approvals.
DNS review remains separate.

- App HTTP/HTTPS checks are grouped by application and direct-node/entrypoint
  location. They use existing local probe grants, never arbitrary manager URLs.
- Loaded-listener certificate expiry and failed automatic issuance/activation
  have their own incident groups. Retained unused certificates do not alert.
- Agent presence, missing/failed HAProxy scrapes and unavailable metrics storage
  are separate evidence. Unknown or stale evidence cannot clear an incident.
  Agent up/collection-time metrics are singleton series without agent-supplied
  labels. Invalid values and duplicate status series are rejected before live
  or historical storage. Reads isolate old malformed node status from healthy
  peers; a foreign organization, cluster or node in a storage response rejects
  the whole response. Historical queries reduce duplicate cardinality in storage
  rather than allowing one node to exhaust the shared response budget.
- Failed updates record verified rollback even when compensation finishes before
  the evaluator runs. Failed recovery stays open until explicit maintenance
  recovery completes or a newer update succeeds.

Evaluation runs every five seconds in the supported managed cluster profile.
App/TLS failures debounce for ten seconds; missing agent/metrics evidence for
thirty. Recovery requires ten seconds of advancing passing evidence. The console
refreshes every five seconds and shows delayed evaluation instead of claiming
health. Configured checks are location-specific and do not establish public
reachability. Expiry warning begins at the smaller of seven days and one third
of the leaf's lifetime; critical begins at the smaller of one hour and one tenth.

State and transition snapshots live in the manager's PostgreSQL. Closed state and
transition history have thirty-day retention; active incidents are retained.
This does not prune configuration versions or append-only actor audit history.

The standalone API is `GET /v1/clusters/{cluster}/incidents`. It returns current
incidents, at most fifty transition snapshots, `evaluated_at_ms`, safe notification
counts and an optional `next_cursor`. Request the next page with `?before=CURSOR`;
`?incident=UUID` filters one incident within the authorized cluster. Responses
never contain webhook URLs or signing keys.

Delivery is off by default. An installation administrator may mount a private
regular JSON file (mode `0600`, at most 64 KiB) and pass
`--incident-webhooks-file /run/secrets/incident-webhooks.json`, or set
`HAPROXY_INCIDENT_WEBHOOKS_FILE` to that path:

```json
[
  {
    "id": "00000000-0000-4000-8000-000000000101",
    "organization_id": "00000000-0000-4000-8000-000000000001",
    "url": "https://alerts.example.com/haproxy",
    "secret": "REPLACE_WITH_BASE64_OF_32_TO_128_RANDOM_BYTES"
  }
]
```

Use a fresh UUID for a destination. The destination ID binds organization and
URL; changing either requires a new ID. A signing-secret rotation retains the ID.
One destination per organization and up to thirty-two per installation are
supported. HTTPS redirects, URL userinfo, query credentials and fragments are
rejected. Install private CA trust in the manager's system trust store when
needed; certificate verification remains enabled.

The receiver gets `{ "version": 1, "event": ... }`, an `Idempotency-Key` of
`DESTINATION_ID:SEQUENCE`, and `X-Converge-Signature: sha256=HEX_HMAC_SHA256` over
the exact body. Verify the signature with the decoded secret and durably
deduplicate the key before performing side effects. New destinations begin after
their registration watermark, without replaying previous incidents. Delivery is
at least once with persistent leases and at most eight attempts, backoff and
bounded Retry-After. The latest event per incident is coalesced behind a
five-minute cooldown; critical escalation bypasses it. Every transition remains
in the console. Removed destinations stop sending after manager restart; failed
delivery counts remain visible for investigation. This is an integration webhook,
not an email or Slack-specific adapter.

For the existing two-app, trusted-HTTPS demo, rebuild the manager and backend
fixture, then replay the incident test:

```sh
python3 - <<'PY'
import sys
sys.path.insert(0, 'scripts')
from pair import compose
compose('build', 'manager', 'pair-backend')
compose('up', '-d', '--no-deps', 'manager', 'pair-backend', 'pair-backend-2')
PY
python3 scripts/incidents.py --prepare
python3 scripts/incidents.py
```

The reversible fixture only fails `/shop/items` while a temporary flag exists in
the backend container. It leaves `/healthz`, direct node checks and app B serving.
The test covers grouped HTTP/HTTPS failure, verified recovery, reopen, manager
restart persistence and agent loss/recovery, with continuous app B traffic.
Flags and paused agents are restored in `finally`; routing documents, listener
pins and node keys are not edited. The replay rejects configured notification
destinations so it cannot send external incident messages.

The preparation command is restricted to the two synthetic apps. It gracefully
reloads each node's exact existing configuration to refresh startup DNS after
Docker recreates app containers; this profile does not provide dynamic service
discovery. It rejects active operations and customized app destinations. Container
restart alone preserves its IP; recreation may require this explicit refresh.

## Application traffic and request investigation

[ADR 0045](adr/0045-haproxy-application-request-telemetry.md) adds
opt-in request metadata, charts and search. In the console, choose an application's
**Traffic and requests** action. Request details show its node, backend server,
HTTP status, timings, observation ID, configuration fingerprint and matching
retained application versions. Application incidents link to this drawer.

For an existing synthetic two-node HTTP/HTTPS lab, build/update the manager,
ingestion and agents, then run `python3 scripts/requests.py` from the repository root.
The script installs the local profile while each node is withdrawn/drained,
enables both demo applications through normal rolling deployments, generates
HTTP/HTTPS errors and latency for app A, and tests log-store outage/recovery and
history while monitoring app B. It leaves both apps collecting for exploration.
It rejects customized lab routing and does not change DNS or send notifications.

Standalone setup uses the same code:

1. Run private VictoriaLogs v1.53.0 and set `HAPROXY_LOGS_URL` (or `--logs-url`)
   on the manager and ingestion processes. Do not publish VictoriaLogs directly
   to customers/the Internet. The local Compose profile retains two days with a
   1 GiB disk budget; set retention and resource budgets for the installation.
2. Give the local agent a writable socket directory shared with its HAProxy
   process, and start it with `--access-log-socket /run/haproxy/access.sock`.
   Existing configuration discovery and node identity are required. The socket
   is mode 0660; grant the HAProxy user the directory's group access. With
   containers, the agent's socket-directory mount must be writable. With the
   hardened systemd service, add that directory to its writable paths.
3. Configure the HAProxy global logger and the selected HTTP frontend explicitly:

   ```haproxy
   global
       log /run/haproxy/access.sock len 2048 format raw local0

   frontend public
       log global
       log-format "cv1 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts"
   ```

   Keep the frontend's existing binds and routing. Validate/reload safely; use
   whole-node maintenance for a shared entrypoint. For DPA, the `log_format`
   string must include the surrounding quotes. Existing customer logging
   remains operator-owned; the application compiler refuses an incompatible
   frontend profile rather than replacing it.

4. Edit the application and enable **Collect request telemetry**, or submit a
   new application revision with `spec.request_telemetry: true`. Version 5 agent
   discovery reports `request_logs_allowed`; unsupported nodes fail before
   rollout dispatch. Configuration fingerprints are compiler-owned, never
   request headers. Disable collection by saving the checkbox off; retained
   history is not deleted immediately.

Read APIs require `haproxy.read` and work with existing standalone operator
credentials or Converge browser login:

- `GET /v1/clusters/{cluster}/applications/{application}/traffic`
- `GET /v1/clusters/{cluster}/applications/{application}/requests`
- `GET /v1/clusters/{cluster}/applications/{application}/geography`

Both accept `range=15m|1h|6h|24h`, `node=UUID`, `scheme=http|https`,
`status_class=1xx|2xx|3xx|4xx|5xx|other`, a classified `method`, `min_ms=0..60000`,
`spec_digest=SHA256` and `request_id=OBSERVATION_ID`. `end=UNIX_MILLISECONDS`
freezes a search interval; pass the response's `end_ms` and `next_cursor` as
`end` and `before` for older requests. Search returns ten rows, newest first.
Late-arriving records can appear on refresh. Raw LogsQL and destination URLs
are never accepted. Lists, results and post-query session checks stay tenant-bound.

Opt-in [local geography](geography.md) adds country and ASN groups, coverage and
node database status. The `country` filter accepts up to 32 distinct comma-separated country codes,
including `unknown` (for example `country=GB,RO,unknown`). Selections are combined
in one scoped query for charts, Top paths and request pagination. Existing
single-country links still work. Typed `asn=15169|0` filters also apply to
all three endpoints; zero ASN includes older/missing observations. The `cv3`
profile keeps source addresses and forwarding headers on the nodes. The Geo API
returns only country/ASN, lookup status and effective dataset/profile metadata.

Percentiles are estimates over recorded HAProxy total request durations, not
backend rolling averages or client round-trip latency. Monitoring requests through
the listener are included. By default raw paths/URLs, query strings, client addresses,
headers/cookies and bodies are excluded. A fingerprint can match multiple
identical application versions; the observation ID is not a client trace ID.

Collection is best effort with no intentional sampling: 2 KiB datagrams, 2,048
queued records, 256-record/256 KiB batches, a separate sender/transport and short
deadlines. No disk spool or ambiguous-write retries are present. Status exposes
per-process received, ignored, rejected, queue-drop and unconfirmed counters
across the node's apps; boot UUID changes distinguish resets. Unconfirmed sends
may have reached storage, and losses before the socket cannot be measured.
An empty log result or listening socket does not prove zero traffic or complete
capture. A store outage preserves HAProxy serving and node metrics/control;
console refresh failures retain prior data and display the failure explicitly.
Production capacity, retention, backup and operational support remain release
gates, not guarantees established by this local smoke test.

### Optional path collection

[ADR 0046](adr/0046-haproxy-local-request-path-policy.md) adds paths
without putting URLs into metric labels. **Top paths** ranks the current filters
by request count, HTTP 5xx count or estimated p95. Select a path to filter charts
and requests. Legacy/withheld values are explicit and cannot be backfilled.

Create an owner-writable regular local file (mode 0644 or 0600), then start the
agent with `--request-path-policy /etc/haproxy-agent/paths.json` alongside its
log socket. Restart the agent after editing the file. Recommended policy:

```json
{
  "version": 1,
  "mode": "sanitized",
  "routes": ["/healthz", "/orders/:id", "/files/*rest"]
}
```

Rules match the entire path, case-sensitively, in order. `:name` replaces one
nonempty segment; terminal `*name` replaces a nonempty suffix. Only the configured
pattern is emitted; unmatched paths are withheld. Do not put secrets in literal
pattern segments. No file, or `"mode": "off"`, withholds paths. Explicit exact
collection with local redaction uses:

```json
{
  "version": 1,
  "mode": "exact",
  "redact": ["/orders/:id", "/reset/:token", "/private/*rest"]
}
```

Exact mode records other valid paths and may contain sensitive identifiers;
choose redaction and retention for the installation. The manager cannot edit or
enable this local policy. Both modes always strip query/fragment text. The agent
decodes once and withholds controls, non-ASCII/oversized paths, encoded slashes,
double encoding and dot segments. Limits: 512-byte paths, 128 rules, 32 KiB files.

Install the frontend profile explicitly, preserving binds and routing:

```haproxy
frontend public
    no log
    log /run/haproxy/access.sock len 2048 format raw local0
    http-request set-var(txn.cv_path) path
    log-format "cv2 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex]"
```

`no log` first clears any inherited logger list. Then use a socket-only frontend target: never forward this intermediate profile to a
remote logger/stdout collector. Raw path bytes stay on the node and the agent
sanitizes before queueing or transport. Existing operator logging is not silently
replaced; upgraded agents still support `cv1`. The console reports each node's
path mode; the API also returns its policy fingerprint.

On a managed HTTPS frontend, preserve the compiler's `cv_https_route` guard
prefix and append the local capture after those guards. Discovery rejects
changed guard ordering. With DPA, retain the surrounding `log_format` quotes.

The existing read APIs add `path=/orders/:id` as an exact collected-value filter
and `path_order=requests|errors|latency` for top-ten ranking. No raw query syntax
or extra permission is added. Paths stay outside VictoriaMetrics labels and
VictoriaLogs stream identity fields. Bounded queries do not prove capacity.

Run `python3 scripts/request_paths.py` after updating the Go image and both agents in the
synthetic two-app HTTP/HTTPS lab. It upgrades profiles under maintenance, verifies
route patterns, failures/latency, filtering/paging and privacy while app B serves,
and preserves shared routing/TLS. The demo's checked-in local policy is
`deploy/local/pair/request-path-policy.json`; collection stays on for exploration.

The optional [outbound Converge connector](converge-connector.md) links a self-hosted manager without a public inbound management port. Local login, storage, agents and traffic continue independently.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
