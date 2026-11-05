# ADR 0044 — HAProxy operational incidents and optional delivery

- Status: Accepted
- Date: 2026-10-26

## Decision

The independent Go manager evaluates fixed operational rules every five seconds,
without a browser session. Its own PostgreSQL stores incident state, debounce
checkpoints, immutable transition snapshots and optional webhook delivery leases.
Phoenix continues to provide identity grants only. No observations, incident
evaluation or delivery pass through Phoenix or Converge's database.

An incident groups one application/protocol/check location, one node, one shared
listener, one automatic TLS plan, or one failed configuration update. Request
failures and TLS problems require ten seconds of continuous evidence; missing
agents, HAProxy scrapes and telemetry service reads require thirty seconds.
Recovery requires ten seconds of advancing healthy observations. Unknown, stale,
wrong-policy, wrong-application or wrong-artifact evidence cannot clear an incident.
Evaluation gaps longer than fifteen seconds reset debounce windows.

Application evidence comes from the existing bounded node-local probe policy.
Direct-node and shared-entrypoint checks remain separate. A peer's response
does not prove another node's health, and neither check proves public
reachability. Unconfigured checks do not imply health. Agent presence uses a
fresh control heartbeat or a fresh accepted scrape timestamp; an unavailable
telemetry service cannot prove that an otherwise unobserved agent is offline.

Expiry rules consider only certificates referenced by loaded shared listeners,
not unused retained certificates. The warning window is the smaller of seven
days and one third of the leaf's lifetime; critical begins at the smaller of
one hour and one tenth of its lifetime. Certificate private keys stay on nodes.
Stale or incomplete listener inventory cannot establish TLS recovery.

Failed deployments remain visible even when automatic compensation finishes
between evaluations. Verified rollback records recovery. Failed compensation
remains active until a newer successful update or completion of every explicit
maintenance recovery handoff. Completing maintenance does not rewrite the failed
deployment or assert application reachability. Application disable/delete,
node revocation and verified listener removal retire their monitoring explicitly.

## API and console

`GET /v1/clusters/{cluster}/incidents` requires tenant-resolved `haproxy.read`.
It returns bounded current incidents, fifty transition snapshots per page, a
strict descending sequence cursor, the evaluation timestamp and safe delivery
counts. Neither webhook URLs nor signing keys appear in the response. The
console retains previously loaded incidents during refresh failures, shows
delayed evaluation explicitly, and uses a drawer for incident details.

There are no acknowledgement, review, approval or DNS mutation endpoints.
Existing `haproxy.manage`, local policy and verification remain the write
boundaries. DNS workflows are unchanged.

Operational timelines and closed incident state have thirty-day retention.
Active incidents are retained until recovery or explicit retirement; ordinary
configuration and actor audit history remains append-only and is unaffected.
Hourly cleanup also expires delivery snapshots older than that operational
window. This is operational incident history, not the organization audit log.

## Optional delivery

Delivery is disabled until an installation administrator configures an explicit
tenant-bound HTTPS destination in a private regular file. There is one destination
per organization, up to thirty-two per installation. Browser users cannot supply
arbitrary outbound URLs or access signing keys. The same configuration works in
standalone and Converge-connected installations.

New destinations start after their registration watermark; they do not replay
existing historical incidents. Stable destination IDs bind the organization and
URL, so changing either requires a new ID. Signing-secret rotation retains that
binding. Removing a target from configuration stops sending to it.

The durable queue coalesces the latest transition per incident and waits five
minutes between sends; critical escalation bypasses that cooldown. Every
transition remains in the console timeline. Leases fence concurrent manager
replicas, retries back off for at most eight attempts, and a lost checkpoint may
repeat delivery with the same destination/sequence idempotency key. Receivers
verify the HMAC-SHA256 body signature and deduplicate that key. Delivery is at
least once, not exactly once. Redirects are rejected, response reads and request
duration are bounded, and errors never log response bodies, URLs or keys.

## Limits

This slice uses the existing two-to-twelve-node managed cluster profile and
existing application/revision bounds. Evaluation is paged across installation
clusters, so the API's evaluation timestamp is the freshness authority; a large
or unavailable installation must not imply a five-second health guarantee.
Request logs, latency/error-rate policies, notification settings in the browser,
custom thresholds, silencing, public probes and additional delivery adapters
remain future work. These rules do not change traffic or remediate it silently.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
