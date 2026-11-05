# ADR 0055 — Node-local client geography

- Status: Accepted
- Date: 2026-10-30

## Decision

Extend private request telemetry with opt-in country and autonomous system
estimates. The operator owns the client-address trust policy and MMDB files on
each node. No client address or forwarded header is sent to the manager,
VictoriaLogs, the standalone console or Converge. No external lookup service,
DNS mutation, approval workflow or WAF enforcement is introduced by this slice.

The host installs the `cv3` log profile, captures the transport peer in
`tcp-request connection` before source rewriting, and supplies an absolute
`--request-geo-policy` file. The policy defaults to absent/off. Direct-peer mode
ignores forwarded input. X-Forwarded-For and PROXY protocol modes require explicit
trusted proxy CIDRs. XFF walks right to left, stopping at the first untrusted
hop; duplicate header fields, malformed addresses, all-trusted chains, mixed
PROXY/XFF chains and oversized input remain unresolved. The agent never guesses
from the leftmost address. Trust is not editable through a cloud browser.

Use the pinned `maxminddb-golang/v2` reader with verified immutable byte snapshots
of operator-provided Country and/or ASN databases. Each file is limited to
32 MiB. The reader checks database structure and the expected dataset type before
lookups. Missing/corrupt databases produce unavailable evidence; metrics, control
and HAProxy serving remain independent. Replacing a policy or database requires
an agent restart. Its enrollment identity and keys are unchanged.

An event exports country code, ASN, the selected address-source class, lookup
status and the effective policy/database fingerprint. A collection heartbeat
exports dataset hashes and build times, but no local paths. Legacy, disabled,
private-address, unmatched and unresolved observations remain distinguishable.
This is best-effort request evidence, not a visitor identity, geographic access
guarantee, exact traffic bill or retroactive enrichment of older records.

The existing bounded log workload and tenant-scoped request API remain its
transport/storage boundary. Batches are bounded by both count and serialized
bytes. A read-only application geography endpoint returns at most ten country
and ten ASN groups, coverage, collection status and request statistics. Typed
country/ASN filters also apply to request search. Both consoles expose the same
Geography application page and request-detail evidence.

Production data files are not bundled or downloaded. Operators choose their data
source and comply with its terms independently of the product's eventual license.
Only upstream synthetic test databases, pinned by revision/checksum with their
MIT notice, are included for tests and the private demo.

## Follow-on work

Country/IP/ASN policies and rate limits need a separately versioned local
security model and rollout verification. WAF inspection follows in observe mode
before enforcement. Neither is inferred from telemetry or enabled by this ADR.

## References

- [HAProxy 3.4 source-address and logging fetches](https://docs.haproxy.org/3.4/configuration.html)
- [Pinned MMDB reader](https://github.com/oschwald/maxminddb-golang/releases/tag/v2.7.0)
- [Upstream synthetic test data](https://github.com/maxmind/MaxMind-DB/tree/d1551916cff0712d12ff602c309234471b0402a2/test-data)

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
