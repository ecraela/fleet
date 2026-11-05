# Linux and Docker agent lifecycle

**Release availability:** Fleet currently publishes source only. The release-tag
placeholders below document the signed installation contract; replace them only
with an exact qualified tag once Fleet artifacts are published. Existing private
RC installations retain their original publisher; see [migration](migration-to-fleet.md).

The manager, ingest service and agents are independently versioned in the public
[`ecraela/fleet`](https://github.com/ecraela/fleet) repository.
The first supported targets are Linux amd64/arm64, using either Docker Compose
or a native systemd service. Existing local Docker enrollment remains available
for development. Current source uses [AGPL-3.0-only with optional commercial licensing](../LICENSING.md).

## Trust a release

Published release bundles contain `release.json`, its Cosign Sigstore bundle,
`agentctl.py`, and standalone Linux agent/manager executables. The manifest binds
the exact version, source revision, protocol/state schema, binary checksums and
GHCR image digest. Published Fleet assets must be downloadable without a GitHub
account. Use one exact qualified Fleet tag in place of `vMAJOR.MINOR.PATCH` below.

The [distribution packaging](third-party.md) also signs
the AGPL text, third-party notices, dependency inventory, matching project source
archive and source file hashes. Use that candidate's authenticated installer to
read the expanded manifest; older host tools reject the new artifact names. The
new installer still accepts older signed releases for rollback. Linux installs
retain the verified legal files beside the pinned executable.

To download the example candidate:

```bash
gh release download vMAJOR.MINOR.PATCH --repo ecraela/fleet --dir ./release-vMAJOR.MINOR.PATCH
cosign verify-blob ./release-vMAJOR.MINOR.PATCH/release.json \
  --bundle ./release-vMAJOR.MINOR.PATCH/release.sigstore.json \
  --certificate-identity https://github.com/ecraela/fleet/.github/workflows/release.yml@refs/tags/vMAJOR.MINOR.PATCH \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Before executing a downloaded installer, verify its checksum against that
authenticated manifest. The console's signed-release command performs both
steps, including verifying `agentctl.py` before executing it. Python 3.11+,
GitHub CLI access and Cosign 3.0.2+ are prerequisites. Docker installs additionally
require Docker Compose v2; native installs require Linux/systemd, root for the
installer, and an unprivileged `haproxy-agent` service account.

An offline distributor may instead sign the manifest with Ed25519 and deliver
`release.sig`. Provision its public key through an independent trusted channel
and pass `--public-key /etc/haproxy-agent/releases.pub`. Never download a public
key next to an untrusted archive and treat that as publisher verification. No
unsigned mode or mutable Docker image tag is supported. Public Sigstore signing
records workflow identity/artifact digests in its transparency service; use the
offline profile if that metadata must remain private.

## Install

In the standalone console or linked Converge console, choose **Enroll node**,
then **Docker Compose** or **Linux service**.
Signed installation choices appear when the installation administrator sets:

```text
HAPROXY_AGENT_RELEASE_VERSION=vMAJOR.MINOR.PATCH
HAPROXY_AGENT_RELEASE_URL=https://github.com/ecraela/fleet/releases/download/vMAJOR.MINOR.PATCH
HAPROXY_AGENT_IMAGE=ghcr.io/ecraela/fleet@sha256:<published-digest>
```

These are public deployment settings, not keys. Configure the manager/ingest
origins and metrics URL as before. The generated command creates private files,
verifies the publisher and exact executable/image, starts the agent, and waits
for successful metrics from that node. It installs observation-only access;
additional local runtime/configuration/TLS policies remain explicit.

For an existing Docker Compose agent, create a local installer profile:

```json
{
  "mode": "docker",
  "state_dir": "/srv/haproxy-agent/identity",
  "install_dir": "/srv/haproxy-agent/software",
  "docker": {
    "project_directory": "/srv/haproxy-agent",
    "files": ["/srv/haproxy-agent/compose.yaml"],
    "service": "agent"
  }
}
```

Use absolute paths; keep installer config non-writable by other accounts and
the installation directory private (0700). The Compose service must explicitly
use `entrypoint: [/usr/local/bin/haproxy-agent]`. Its existing volumes, UID,
groups, network, limits, flags and policy files are preserved. Only the selected
service's image is overridden. The host runs Docker; never mount its socket
inside the agent. Remote Docker contexts/host overrides are refused.

For Linux, create a `linux` profile with `state_dir`, `install_dir`, `user`, and
an `arguments` array containing the ordinary agent flags, including the exact
`--state-dir`. The generated systemd service runs unprivileged with a read-only
system and a bounded writable identity directory. Grant additional local socket
or configuration directories only with explicit `writable_paths`; grant the
service account the corresponding filesystem permissions. Put CA/credential
files under `/etc/haproxy-agent`, not a protected home directory. Do not mount or
configure host management credentials in the agent.
The native profile supports one agent service per host and refuses a unit owned
by a different installation. Reuse its existing profile for upgrades.

```bash
python3 -I ./release-vMAJOR.MINOR.PATCH/agentctl.py install \
  --config /srv/haproxy-agent/agentctl.json \
  --release ./release-vMAJOR.MINOR.PATCH --version vMAJOR.MINOR.PATCH
```

An initial install can adopt an already enrolled identity. It never rewrites
existing node keys. Keep the same profile, installer state and trust root for
future upgrades. Trust-root rotation is a separate local provisioning operation.

## Upgrade and roll back

Download and verify the next bundle, then upgrade **one agent at a time**.
`v0.1.1` below is a placeholder for a future compatible release, not a currently
published tag. Replace it with the exact supported version you are deploying:

```bash
python3 -I ./release-v0.1.1/agentctl.py upgrade \
  --config /srv/haproxy-agent/agentctl.json \
  --release ./release-v0.1.1 --version v0.1.1
python3 -I ./release-v0.1.1/agentctl.py status --config /srv/haproxy-agent/agentctl.json
python3 -I ./release-v0.1.1/agentctl.py rollback \
  --config /srv/haproxy-agent/agentctl.json \
  --release ./release-v0.1.0-rc.9 --version v0.1.0-rc.9
```

Use `sudo` for the native service. HAProxy continues serving during agent-only
restarts; upgrading HAProxy itself is a separate maintenance/drain operation.
Both old and new releases must support protocol 1 and state schema 1. Store
identity, command receipts, configuration artifacts, TLS/ACME account/order state
outside the software installation directory and include them in backups.

Invoke downloaded host tools with `python3 -I`, including verification snippets.
Isolated Python excludes the current directory, `PYTHONPATH` and user packages
from imports. The hardened entrypoints refuse a direct non-isolated invocation.
Keep the current authenticated host tool when rolling back an older agent image;
the runtime release passed with `--release` can differ from the tool's release.

Readiness requires fresh successful HAProxy scrape/export evidence with the
expected version, source revision, executable digest and original node ID.
Missing/corrupt identities fail closed during upgrades. Failed upgrades restore
the previous executable/image and verify it. The previous image is retained
locally; do not prune it before the rollback window closes. Software rollback
never rewinds certificates, node keys or operation journals. Retain release
bundles and image archives for offline recovery; the installer does not provide
a remote backup service or automated image retention policy.

A durable pending marker blocks changes after interruption. Recover it with:

```bash
python3 -I agentctl.py recover --config /srv/haproxy-agent/agentctl.json
```

If recovery fails, the marker stays visible for local intervention. Do not
delete identities, re-enroll or restore stale journals to bypass it. Inspect
`journalctl -u ecraela-haproxy-agent` or the agent's Docker logs locally.

## Qualification

`make agent-releases` (Converge: `make haproxy-agent-releases`) tests signed
fixture installs, sequential upgrades, broken-start automatic rollback and
explicit rollback on both demo nodes, while both applications serve HTTP/HTTPS.
It verifies preserved node IDs/private keys, application config and version
history, and checks reported executable versions. Each run uses a fresh local
fixture signer; its private key is deleted. Retained manifests/public key and
installer state are under private `deploy/local/state.local/pair/.../agent-releases`.
The final compatible fixture version is pinned in the demo Compose overlay.

The independent CI also runs actual native Linux/systemd install, upgrade,
rollback and restart against a copied synthetic node identity on an ephemeral
Linux runner. The Docker agent is stopped before that copy is started; there are
never two active agents for the same identity. Test copies are deleted and the
original Docker agent resumes afterward. This does not claim macOS has systemd.

`python3 scripts/prepare_release_lab.py` prepares the minimal two-app, trusted
HTTP/HTTPS and real local DNS-01 fixture for lifecycle qualification. Full
application mutation, TLS renewal/failure and recovery scenarios run separately
in the acceptance workflow. Bootstrap waits for actual reported configuration
and local ACME grants before issuing changes.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
