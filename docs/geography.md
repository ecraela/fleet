# Country and network visibility

Geography is optional request telemetry. Lookup runs on each HAProxy node using
local Country/ASN databases. The manager receives country codes, autonomous
system numbers, lookup status and database versions. Client IPs and forwarded
headers never leave the node. Nothing in this setup blocks application traffic.

## Prerequisites

- Enable request collection on the application, using the existing permissioned
  rolling deployment.
- Use rc.8 or later for the manager, ingest and relay. The corrected `cv4` profile
  and `check-geo` command require an updated agent from this revision. Older `cv1`/`cv2` records
  remain searchable and have unknown geography.
- Obtain a GeoIP2/GeoLite2 Country database and/or GeoLite2 ASN database under
  terms appropriate for your installation. The product does not ship or download
  production data. City, coordinates, visitor identity and organization names
  are not collected.
- Place the files on the node, readable by the agent and writable only by their
  owner. Each file is limited to 32 MiB. Budget memory for their immutable
  snapshots in addition to the agent's normal workload; both files may use up
  to 64 MiB. Do not increase access to the host's keys or Docker socket.

## Choose the client-address source

In either console, open an application's **Geography** page and choose
**Client IP & Geo setup**. The drawer guides the traffic-path choice, validates
explicit proxy CIDRs, generates a local policy and logging snippet, and checks
new request evidence per node. It makes no remote configuration or trust changes.
Both Linux service and Docker instructions preserve the existing identity.

Create an owner-writable local JSON file such as
`/etc/ecraela-haproxy-agent/geo.json`:

```json
{
  "version": 1,
  "mode": "peer",
  "country_database": "/etc/ecraela-haproxy-agent/geo/country.mmdb",
  "asn_database": "/etc/ecraela-haproxy-agent/geo/asn.mmdb"
}
```

Either database path may be omitted. Direct-peer mode always uses the captured
transport peer, ignoring XFF and PROXY claims. Behind an ingress proxy it shows
the proxy's geography, not the original visitor.

For one trusted HTTP proxy chain, choose `"mode": "xff"` and add
`"trusted_proxies": ["10.10.0.0/24"]`, replacing that example with your actual
proxy addresses. The ingress must append the observed client address or replace
untrusted incoming forwarding headers. The agent walks the chain from the
transport peer toward the client, right to left, and stops at the first
untrusted address. Input supplied before that hop cannot override it. An
untrusted direct peer's header is ignored. More than one XFF header field,
non-IP tokens, ports, scoped IPv6, more than 16 hops, more than 256 header bytes
or an all-trusted chain produce unresolved evidence.

For an ingress using PROXY protocol, choose `"mode": "proxy_protocol"` with
explicit trusted ranges. The listener's host-owned PROXY configuration and
network restrictions must already be correct. The captured transport peer must
be trusted and the connection must actually have supplied PROXY protocol.
Use the conditional receiver below, replacing the example with the actual
ingress address, and enable `send-proxy-v2` on the upstream's existing server
lines. Keep the peer capture first:

```haproxy
    tcp-request connection set-var(sess.cv_peer) src
    tcp-request connection expect-proxy layer4 if { var(sess.cv_peer) -m ip 10.10.0.10/32 }
    tcp-request session set-var(sess.cv_client) src
```

Do not combine this profile with an unconditional `accept-proxy` bind. Untrusted
peers cannot submit PROXY headers; ordinary direct health probes can still
connect. Trusted peers must supply a valid envelope before HTTP or TLS.
Restrict listener reachability to ingress and intended probes at the network
boundary. A PROXY header is an assertion by that peer, not authentication.
Coordinate sender and receiver changes while each node is drained. Pool-wide
settings need a separate receiving listener and target pool for migration.
Probes from trusted ranges must also send PROXY or use a separate private probe
listener. Managed node maintenance pins its entrypoint; use the ingress owner's
coordinated maintenance procedure when changing that sender. Editing the pinned
entrypoint pauses the operation rather than silently adopting new configuration.
Never expose an unrestricted PROXY listener to untrusted clients. Combining
PROXY protocol and XFF trust is outside this profile and remains unresolved in
XFF mode. `/0` blanket trust ranges are refused.

## Install the local logging profile

Keep the existing private Unix datagram target, permissions and 2048-byte log
limit from [request telemetry](observability.md). On the intended HTTP
frontend, place the peer capture before any other connection source rewrite and
capture XFF before any request-header rewrite. Keep managed TLS guards as the
HTTP rule prefix; place the HTTP captures after those guards:

```haproxy
    no log
    log /run/haproxy/access.sock len 2048 format raw local0
    tcp-request connection set-var(sess.cv_peer) src
    tcp-request session set-var(sess.cv_client) src
    http-request set-var(txn.cv_path) path
    http-request set-var(txn.cv_xff_count) req.fhdr_cnt(X-Forwarded-For)
    http-request set-var(txn.cv_xff) req.fhdr(X-Forwarded-For)
    log-format "cv4 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex] %[var(sess.cv_peer)] %[var(sess.cv_client)] %[fc_rcvd_proxy] %[var(txn.cv_xff_count)] %[var(txn.cv_xff),length] %[var(txn.cv_xff),hex]"
```

Do not replace a customized listener or its log destinations silently. Validate
with `haproxy -c`, withdraw/drain when changing a serving node, then reload,
health-check and rejoin one node at a time. Shared TLS and routing stay intact.
Path collection still follows its separate local policy; no path is exported by
default. Oversized/malformed datagrams are rejected and exposed in the existing
collection loss counters.

`no log` clears inherited loggers before the private target is added. A frontend
target alone does not remove an inherited `log global`; without the reset, raw
addresses/headers may also reach stdout or a remote logger, and records can be
duplicated. In PROXY mode add the conditional `expect-proxy` rule after the peer
capture above, before any other source rewrite.

Upgrade the manager and ingestion services before enabling Geo on agents.

Append `--request-geo-policy /etc/ecraela-haproxy-agent/geo.json` to the agent's
existing command and restart it, preserving all identity, socket and policy
arguments. Do not re-enroll or replace its state directory. A malformed trust
policy is a startup error. An absent, invalid, oversized or incompatible MMDB
file instead reports database unavailable while the agent's other workloads
continue. There is no fallback to a remote lookup or a guessed country.

Before restarting, check the policy as the agent user with its actual mounts:

```sh
haproxy-agent check-geo --request-geo-policy /etc/ecraela-haproxy-agent/geo.json
```

This command only opens the policy and database snapshots. It never enrolls,
creates an identity, connects to HAProxy or sends data. It prints the effective
mode, safe dataset metadata and profile fingerprint, and exits unsuccessfully
for malformed policy, disabled collection or unavailable data.

`cv4` captures `src` in the TCP session phase after PROXY decoding, before any
session/application source rewriting. `fc_src` in the legacy `cv3` profile is a
connection address, so legacy PROXY-mode records are now unresolved rather than
incorrectly locating the proxy. Legacy peer/XFF records remain readable. Upgrade
the agent before installing `cv4`; an older parser rejects unknown formats.

## Read the results

Open an application, then **Geography** in either console. Country and network
tables include unknown groups, recorded request totals, coverage and HTTP error
counts. **View requests** applies the selected country/ASN to request search;
request details show the selected address-source class and lookup status.
Collection status shows each node's effective profile and database build dates.
The API also returns the exact dataset hashes.

The setup drawer checks new requests against each node's current profile and
chosen address source. Stale reports, earlier requests and a ready database alone
cannot complete it. The manager supports a typed `client_source` request filter,
so frequent direct probes cannot hide an occasional forwarded request. Older
managers retain their existing read API without this filter. Compare the profile with `check-geo` output. This does not
attest the proxy configuration, exact CIDRs or firewall; test forged headers and
unauthorized PROXY senders at the actual ingress too.

Different nodes can have different profiles; compare their collection rows.
Unresolved chains, private/local addresses, disabled collection, old formats,
database misses and storage loss are not evidence of a location or of zero
traffic. Geography estimates are not proof of a person's physical location.

Replace databases using an operator-controlled update process, verify their
provenance, then restart the agent. The current immutable snapshot remains in
use until restart; the effective fingerprint changes with the data. Include the
Geo policy and data directory in the node backup's explicit host-owned paths.
The manager's backup cannot recover files or private keys it never held.

## Two-node demo

After building the current image, `HAPROXY_GEO_IMAGE=converge-haproxy:local make
geography` exercises the two existing synthetic applications over HTTP and
trusted HTTPS. It uses the small upstream test MMDBs in
`internal/accesslog/testdata`, not production geolocation data. The test sends
controlled forwarding chains and checks unknown handling, spoof resistance,
country/ASN filters, stored history and uninterrupted neighboring-app traffic.
It also sends real PROXY v2 through the shared entrypoint and PROXY v1 from a
private fixture over HTTP and HTTPS, verifies both apps/nodes, rejects untrusted
or missing envelopes, and checks that mixed PROXY/XFF trust stays unresolved.
No DNS provider or external lookup is used.

The private fixture client is the only trusted forwarding hop during the test.
The existing TCP entrypoint is not trusted to sanitize HTTP headers. The scenario
returns both agents to direct-peer mode, restores the original entrypoint and
receiver PROXY settings, and removes the fixture clients afterward.
The opt-in demo overlay, local test databases and evidence remain in ignored
`deploy/local/state.local/pair/<organization>/geo`. Use `HAPROXY_DEMO_ROOT` when
testing the preserved lab from a separate source checkout.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
