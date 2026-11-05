# ADR 0057 — Guided client address and Geo setup

- Status: Accepted
- Date: 2026-10-31

## Decision

Keep the host-owned trust boundary from ADR 0055. Both consoles provide the same
three-step drawer: choose direct peer, trusted HTTP forwarding or trusted PROXY
protocol; generate local policy and logging instructions; observe fresh request
evidence from each node. This adds no remote policy writer, approval workflow,
raw address storage or Converge dependency.

The generated PROXY profile captures the transport peer first, then uses
conditional `tcp-request connection expect-proxy layer4` for explicit CIDRs.
After decoding, a TCP session rule captures the accepted client before source
rewrites. The additive `cv4` format uses both captures; legacy `cv3` PROXY
observations are unresolved because its connection-address field cannot prove
the forwarded client. Peer/XFF and older stored records remain readable.
It does not use an unrestricted `accept-proxy` listener. Sender configuration and
network restrictions remain the host operator's responsibility. HTTP forwarding
requires a proxy which sanitizes or appends observed addresses; TCP passthrough
does not provide that guarantee. Mixed PROXY/XFF trust remains unresolved.

Private cv4 logging clears inherited destinations with `no log` before adding its
single bounded Unix socket target. Existing custom logging must be reconciled
explicitly rather than overwritten. This avoids exporting intermediate address
fields to inherited stdout/remote targets or duplicating request evidence.

`haproxy-agent check-geo` is a read-only local check using the same policy loader
as the running agent. It exports only existing safe collection metadata and the
profile fingerprint. It does not require enrollment or create/open identity state.
An unavailable database, disabled policy or invalid policy is an unsuccessful
check, independently of the running agent's other workloads.

The drawer reads existing tenant-scoped geography and request endpoints only
while verifying. An additive, enumerated `client_source` filter keeps direct
health probes from hiding forwarded requests, with tenant scope and stored-result
validation unchanged. Older managers omit the capability flag and retain the
existing read path. A node needs a fresh listening report, a ready database, the
selected mode and a new request with the current profile and address source.
Private addresses and database misses may still establish the source. Readiness
does not attest the exact proxy ranges, firewall or forwarding behavior; operators
compare the local fingerprint and test spoof rejection on their own ingress.

The local two-node scenario covers actual v1/v2 envelopes, HTTP/TLS, unauthorized
and missing envelopes, forged XFF, mixed trust, dataset failures and restoration
of the original PROXY settings, with continuous neighboring-app traffic. Tests stay in the local synthetic lab; remote deployments are outside their scope.

## References

- [HAProxy 3.4 conditional PROXY reception](https://docs.haproxy.org/3.4/configuration.html#4.4-tcp-request%20connection)
- [HAProxy PROXY protocol](https://www.haproxy.com/documentation/haproxy-configuration-tutorials/proxying-essentials/client-ip-preservation/enable-proxy-protocol/)

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
