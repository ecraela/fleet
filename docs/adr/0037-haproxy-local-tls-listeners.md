# ADR 0037: Node-local HTTPS listener and certificate references

Status: Accepted

Date: 2026-10-25

Extend ADRs 0035–0036 with permissioned HTTPS deployment on an existing HTTP
frontend. Customers own HAProxy, application workloads, certificates and traffic
networking. Ecraela supplies management, with the same Go code for hosted and
self-hosted installations. HAProxy edits use permissions, local policy and the
existing management MFA gate, without reviews or approvals.

## Local authority and secret boundary

An explicit agent `--tls-policy-file` grants named frontend/address/port bindings
and maps certificate aliases to absolute, immutable local PEM paths. A locally
configured optional `probe_host` supports agents running in a separate container
network namespace; a remote request cannot choose this host or a path. Certificate
files must be visible at the same path to the agent and the local DPA/HAProxy.
Configuration control remains an independent opt-in.

Only aliases, public leaf fingerprints, DNS SANs, validity dates, bounded errors
and loaded-certificate observations cross the control stream. Private keys,
certificate files, local credentials and full configuration snapshots stay on
the nodes. There is no upload, remote filesystem API or remote certificate fetch.
File distribution, permissions and renewal are the customer's responsibility.

The initial profile requires the same alias and SHA-256 leaf identity on every
target. The agent checks the key pair, chain linkage, validity and coverage of
every application hostname on the frontend before applying. It checks again
immediately before commit. Retain immutable old filenames for rollback: HAProxy
can cache loaded certificate paths, so rotation selects a new alias/path rather
than replacing bytes behind an existing filename. Local administrators must not
mutate files while a deployment or rollback uses them.

## Ownership and activation

The bounded typed document adds HTTPS listeners (at most four per node). This
slice attaches a named `cv_https` bind to the cluster's declared existing HTTP
frontend, preserving its HTTP binds and fallback. New arbitrary frontends, raw
TCP/SNI routing and redirect policy are outside this profile.

The agent uses pinned DPA 3.4.5 full-section frontend operations in the same
versioned transaction as application/backend changes. A bounded description
marker records safe typed intent. Discovery verifies the actual bind and owned
request-rule prefix; drift or reserved-name collisions block writes. Node-local
unmanaged fields and rules are preserved. Generic node/backend writers cannot
change listener ownership or restore a full snapshot over managed apps/TLS.

HTTPS requires TLS 1.2 or newer and strict SNI. An owned request-rule prefix only
admits a matching SNI, HTTP Host and application path. Unknown HTTPS routes and
Host/SNI mismatches return 404 instead of reaching the existing HTTP fallback.
Changing an application recomposes this prefix along with its routing rules.

The manager stores immutable, tenant/cluster-bound listener revisions and durable
rollout references in its own PostgreSQL database. Deploy, rotate, disable and
restore all use the existing sequential withdrawal/drain/update/health/rejoin
coordinator, including automatic compensation. An explicit restore creates a new
revision and composes the selected listeners with current applications/backends;
expired or missing old material is rejected again. Idempotency, artifact/version
preconditions, fencing and unknown-outcome handling match application deployment.

## Verification and operating limits

A bounded agent probe connects to the locally granted listener (or local policy's
sidecar host) and requires the exact expected leaf fingerprint and hostname. The
pin supports private PKI without uploading trust roots. This is loaded identity
evidence, not public CA trust, chain trust or external reachability evidence.
The probe cannot turn into a manager-selected network destination. Existing
process/artifact/backend-health windows must also pass before rejoining.

The demo's entrypoint passes TCP on 8080 and 8443 to the same two server identities,
forwarding the incoming destination port. Both ports therefore follow the same
maintenance controls. Its node agents independently terminate HTTPS; the manager
and ingestion remain outside the traffic path. `make haproxy-tls` checks trusted
HTTPS for two apps, hostname/path isolation, invalid certificates, rolling
rotation during traffic, manager restart and rollback. The private demo CA is
only trusted explicitly by the test client.

ACME, certificate distribution/garbage collection, arbitrary listener creation,
public reachability probes, provider LB adapters and production packaging remain
separate roadmap increments.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
