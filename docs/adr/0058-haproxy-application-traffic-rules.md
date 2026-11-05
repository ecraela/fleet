# ADR 0058 — Application traffic rules

- Status: Accepted
- Date: 2026-11-01

## Decision

Attach a versioned ordered traffic policy to the existing HTTP application
intent. Rules combine optional path scope with IP/CIDR, country and ASN matches.
Values within one signal are alternatives; different signals must all match.
The first matching rule selects allow or block. Observe mode records that same
decision without blocking. Enforce mode returns HTTP 403 for a block. Unmatched
traffic continues; unresolved required client/dataset lookups use an explicit
allow/block fallback after the ordered rules. An earlier allowlist can exempt
traffic from that fallback. This is HTTP access control, not payload inspection.

Rules execute in the selected owned application backend, after frontend routing
and TLS guards. A longer-path neighboring application retains its own backend
and policy. There is no per-request dependency on the agent, manager or cloud.
Control uses application versions, host-granted configuration authority,
permissions, existing node maintenance and verification, and rollback. There is
no HAProxy approval workflow. Old agents cannot silently accept policy intent.
Once released, retain the compiler for stored policy versions; changes to its
owned representation require compatibility handling or a new policy version.

## Host authority and assets

The host explicitly enables `--traffic-rules-policy`. Its JSON grant references
the same local client-IP policy used by Geo telemetry, if telemetry Geo is
enabled, the private logger path in HAProxy's namespace, and an owner-writable
directory shared read-only with HAProxy. IP-only
policies may omit databases. A browser cannot change proxy trust, install a data
source, supply local paths, or expand the filesystem grant.

The agent verifies local MMDB snapshots, compiles public prefixes into immutable
content-addressed native HAProxy maps, and stages them before configuration
activation. Compilation is bounded to two million networks and 64 MiB per map;
the directory holds at most sixteen files and 256 MiB. Existing map contents
must match exactly. No automatic pruning removes assets needed for rollback.
Node backups must include the map directory as well as configuration and agent
identity/history. No client-address observation is written to these maps.

Maps remain loaded when management is unavailable. Missing required datasets,
local grants or capture profiles reject deployment. Replacing host trust or
datasets while rules are active is outside this initial asset-update workflow:
remove policies through a verified rollout first, replace/restart locally,
then redeploy. Preserve prior files for rollback; do not edit a hash-named map.

Direct peer and conditional PROXY protocol use pre-decoding transport and
post-decoding accepted-address captures. XFF checks one bounded unchanged header,
validates every token, and walks right to left through explicit trusted peers.
Duplicate/malformed, all-trusted and mixed PROXY/XFF chains stay unresolved.
Rewritten XFF is unresolved rather than inferred from different evidence.
Untrusted peers cannot override their address with forwarding claims.

## Evidence and interface

The additive private `cv5` logging profile appends compiler-owned rule and
decision identifiers. No client IP, forwarding header, arbitrary expression or
map path enters the management protocol or request store. Ingestion validates
decisions against the attributed application's exact recorded specification.
Older records remain readable. Counters are bounded recorded-request aggregates,
with collection loss and missing evidence remaining distinct from zero matches.

Both consoles use an application Traffic rules page and editing drawer, with
explicit order, unknown handling, observe/enforce mode and links to version
history and filtered request investigation. There are at most eight rules per
policy and sixteen values per condition; the existing 16 KiB configuration-intent
bound remains in force. Raw HAProxy syntax is not accepted.

## Verification

Unit and tenant-bound API tests cover typed input, unsupported nodes, local
authority, rule attribution, precedence, unknown handling and immutable assets.
The local two-node scenario exercises native HTTP/HTTPS decisions, spoofed
forwarding, observe mode, ordered exceptions, path isolation, rolling updates,
rollback and a manager outage while application B continues serving. The
Hetzner deployment and original soak deadline remain outside these tests.

## References

- [HAProxy 3.4 configuration](https://docs.haproxy.org/3.4/configuration.html): native IP maps, request ACLs, variable actions and deny.
- [Pinned MMDB traversal API](https://github.com/oschwald/maxminddb-golang/tree/v2.7.0).
- [ADR 0055](0055-haproxy-local-geography.md) and [ADR 0057](0057-haproxy-trusted-client-setup.md).

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
