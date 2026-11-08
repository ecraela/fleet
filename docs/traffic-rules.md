# Application traffic rules

Open **Applications → View application → Traffic rules** in either console.
An operator with `haproxy.manage` can deploy ordered IP/CIDR, country and ASN
rules. Changes use application versions and the existing rolling deployment:
withdraw, drain, update, verify and rejoin each node. No approval is required.
DNS publication and cleanup retain Converge's ordinary review flow.

## Set up the nodes

Upgrade the manager, ingest, optional relay and every target agent together to a
build with application control version 6. A previous release cannot read or
deploy this new intent safely; do not leave old manager processes writing to the
same database. Preserve their storage, identities and keys during the upgrade.

Enable the application's private request collection. Complete the
[client IP and Geo setup](geography.md) first. Use actual ingress ranges; an
arbitrary incoming X-Forwarded-For value must never determine the client.
Country and ASN rules require the corresponding local MMDB dataset on every
target. Data is supplied by the host administrator under its provider's terms;
the manager does not download a production dataset.

The **Node setup** drawer shows the required local grant. Create it as an
owner-writable regular file, readable by the agent, for example:

```json
{
  "version": 1,
  "client_ip_policy": "/etc/ecraela-haproxy-agent/geo.json",
  "maps_directory": "/var/lib/ecraela-haproxy-agent/traffic-maps",
  "haproxy_log_socket": "/run/haproxy/access.sock"
}
```

Add `--traffic-rules-policy /etc/ecraela-haproxy-agent/traffic.json` to the
existing agent command (or set `HAPROXY_TRAFFIC_RULES_POLICY`). Keep its other
arguments and state directory. `client_ip_policy` must be the same local file
used by `--request-geo-policy` when Geo telemetry is enabled. For IP-only rules,
the file may contain only `{"version":1,"mode":"peer"}`.

Create the map directory before starting the agent. Its owner must be able to
write it, other users must not be able to write it, and HAProxy must be able to
read the generated files. For Docker, mount that directory at the **same
absolute path** in both containers: writable in the agent, read-only in HAProxy.
Grant no Docker socket or additional key access. Back up this directory with
the node's HAProxy configuration and agent state; maps are rollback assets.
Use persistent storage rather than a temporary container layer.

`haproxy_log_socket` is the private logger's path **as seen by HAProxy**. The
agent's `--access-log-socket` may name a different path to the same shared Unix
socket. Keep the existing `no log`, raw Unix logger and 2048-byte limit. Use the
Geo capture rules, including accepted-client capture at `tcp-request session`,
and change the fixed log format to `cv5` with the decision suffix:

```haproxy
log-format "cv5 %b %[var(txn.cv_spec)] %s %HM %ST %B %Ta %Tr %Tc %Tw %[ssl_fc] %ts %[var(txn.cv_path),length] %[var(txn.cv_path),hex] %[var(sess.cv_peer)] %[var(sess.cv_client)] %[fc_rcvd_proxy] %[var(txn.cv_xff_count)] %[var(txn.cv_xff),length] %[var(txn.cv_xff),hex] %[var(txn.cv_rule)] %[var(txn.cv_decision)]"
```

The drawer reports each node's grant, client source and dataset readiness. It
does not change trust, listener capture rules or local files. Validate local
HAProxy changes and coordinate reloads while each node is drained. A missing
grant, unsupported agent or required dataset prevents deployment.

## Create and test a policy

Start in **Observe** mode. Add a rule named, for example, “Blocked countries”,
action **Block**, country codes `RU`, and choose how unresolved lookups behave.
Observe records `would_block` while requests continue. Inspect match counts and
**View requests**, then switch to **Enforce** to return HTTP 403 on a block.
This classifies an address using the installed dataset, not the visitor's
nationality or physical location; VPNs and data quality affect the result.

Rules run in order, and the first matching rule wins. Use an earlier **Allow**
rule for a deliberate exception. Values within one condition are alternatives;
different conditions on a rule must all match. A rule can be scoped to a literal
path prefix inside the application. A longer-path neighboring application keeps
its own policy on the shared listener.

There are at most eight rules, sixteen values per condition and 128 characters
in a path prefix. CIDRs must be canonical. `/0` is allowed as an intentional
traffic match, but remains forbidden as a blanket proxy-trust grant. Raw
HAProxy ACLs, file paths and expressions are not application inputs.

Unmatched requests continue. If client resolution or a required lookup is
unavailable and no earlier rule matched, **Unknown client or lookup** selects
allow or block. With block, legitimate private probes may also be rejected.
This fallback covers the whole application, including paths outside an
individual rule's optional path scope.
Allow includes malformed forwarding evidence. If blocking is a security
requirement, configure the trusted ingress to replace incoming forwarding
headers and choose unknown handling deliberately rather than relying on Geo
classification for every address.
Keep representative HTTP/HTTPS rollout checks enabled: a policy that breaks
them automatically rolls back, and a recovery that cannot be verified leaves
the node withdrawn for explicit attention.

**Version history** can restore an earlier policy through a new rolling
deployment. The application menu can remove its policy. Disabling or deleting
the application follows its existing lifecycle and preserves shared TLS and
other applications.

## Evidence, availability and local assets

HAProxy evaluates native rules and immutable local maps without a per-request
call to the agent or manager. For a country or ASN policy, the agent derives a
map containing the selected values plus a compact marker for other known
values. Addresses with no dataset match remain absent, so **Unknown client or
lookup** keeps its meaning. IP-only policies load no Geo map; country-only
policies load no ASN map. The full dataset snapshots remain on disk for later
policy versions and rollback. Loaded rules keep working during management or
telemetry outages. The agent exports only rule IDs, decisions and coarse Geo
evidence; raw client IPs and forwarded headers remain node-local.

Counts describe recorded requests for the exact application specification and
selected range. Collection loss and stale/missing nodes are shown separately;
these are not billing-grade totals. Request search accepts typed
`traffic_rule`, `traffic_decision` and `spec_digest` filters. Older request
formats remain searchable without invented rule evidence.

The agent compiles at most two million networks and 64 MiB per map. The shared
directory holds at most sixteen immutable maps and 256 MiB; automatic pruning
does not remove files needed for rollback. Preserve these assets in backups.
Changing the host's trust or dataset snapshot under an active policy is outside
this initial update workflow. Remove active policies through verified rollouts,
replace the local assets, restart the agent and redeploy. Never edit a
hash-named map in place.

## Local acceptance scenario

Prepare the existing two-app HTTP/TLS pair and geography fixture, build the
current local runtime image, then run:

```sh
HAPROXY_TRAFFIC_IMAGE=your-local-runtime-image python3 scripts/traffic_rules.py
```

This operates only on the selected local Docker pair. It checks native
HTTP/HTTPS decisions, ordered exceptions, path isolation, peer/XFF/PROXY trust,
unknown handling, rollback, a management outage and private evidence while app B
keeps serving. It restores the original application and trust settings and
preserves identities and keys. Synthetic MMDB fixtures are not production Geo
data. No Hetzner or production deployment is selected by this script.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
