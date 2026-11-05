# Standalone Linux/Docker installation and recovery

**Release availability:** Fleet currently publishes source only. The release-tag
placeholders below document the signed installation contract; replace them only
with an exact qualified tag once Fleet artifacts are published. Existing private
RC installations retain their original publisher; see [migration](migration-to-fleet.md).

This customer workflow needs neither Converge nor a source checkout. The Go
manager serves its frontend and local login; HAProxy traffic stays on your nodes.

The first profile supports Linux amd64/arm64, Docker Engine, Compose v2+, Python
3.11+, OpenSSL, age 1.2+ and Cosign 3+ from trusted package sources. Native
Linux/systemd agents use [agentctl](agent-releases.md); native manager packaging
is deferred. Published Fleet releases must be downloadable without GitHub or registry credentials.

The initial host qualification targets Ubuntu 24.04. Other distributions,
including SELinux-specific bind-mount policies, need their own qualification.

Start with a dedicated 2-CPU, 4-GiB host and 20 GiB free persistent disk. These are
initial deployment requirements, not measured capacity guarantees. Metrics/log
retention is seven days; logs also have a 1-GiB cap. Allow additional disk for
logical dumps, cold copies and private restore staging.

## Authenticate an exact release

Installation requires a qualified release from `ecraela/fleet`. A source snapshot
or a release from the private predecessor does not meet that requirement.

```sh
gh release download vMAJOR.MINOR.PATCH --repo ecraela/fleet --dir ./release-vMAJOR.MINOR.PATCH
cosign verify-blob ./release-vMAJOR.MINOR.PATCH/release.json \
  --bundle ./release-vMAJOR.MINOR.PATCH/release.sigstore.json \
  --certificate-identity https://github.com/ecraela/fleet/.github/workflows/release.yml@refs/tags/vMAJOR.MINOR.PATCH \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Before executing either downloaded Python file, check both files and the storage
profile against that authenticated manifest using trusted Python:

```sh
python3 -I - <<'PY'
import hashlib, json
from pathlib import Path
p = Path("release-vMAJOR.MINOR.PATCH")
m = json.loads((p / "release.json").read_text())
assert m["version"] == "vMAJOR.MINOR.PATCH"
for name in ("selfhostctl.py", "agentctl.py", "selfhost.json"):
    assert hashlib.sha256((p / name).read_bytes()).hexdigest() == m["artifacts"][name], name
print("Installer and storage profile authenticated")
PY
```

Use `python3 -I` for every host-tool invocation. It excludes unsigned sibling
modules, `PYTHONPATH` and user packages from imports. The hardened host tool also
checks its bundled `agentctl.py` against its own embedded digest and executes
only those verified bytes. Keep both files together. Use this current tool to
install or roll back older signed runtime releases instead of executing their
older installer. Older host tools without this isolated bootstrap are not
compatible with `-I`; authenticate the hardened tool separately first.

Offline distributions instead verify release.sig using a publisher Ed25519
public key provisioned independently, then perform the same checksum check:

```sh
openssl pkeyutl -verify -pubin -inkey /etc/haproxy-manager/releases.pub \
  -rawin -in ./release-vMAJOR.MINOR.PATCH/release.json -sigfile ./release-vMAJOR.MINOR.PATCH/release.sig
```

For that trust profile, add --public-key /etc/haproxy-manager/releases.pub to
release operations. Never trust a key just because it arrived beside an archive.
Retain bundles and pinned images for recovery. The installer retains local
images, but explicit image pruning can still remove them. Offline hosts must
preload exact images. There is no unsigned or latest-version install.

## Install

Create a mode-0600 password file from your password manager, with 15–1024
password bytes. Never place the password in arguments/history. Create the parent
directory; the installation target itself must not exist. The owning account
needs local Docker access. Root installations use UID/GID 10001 for Go services;
otherwise they use the operator's nonzero UID/GID.

```sh
python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py install \
  --directory /srv/haproxy-manager \
  --release ./release-vMAJOR.MINOR.PATCH --version vMAJOR.MINOR.PATCH \
  --origin https://haproxy.internal.example:8443 \
  --bind-address 10.0.0.10 --manager-port 8443 --ingest-port 9443 \
  --admin-username admin@example.net \
  --password-file /run/secrets/haproxy-initial-password
```

Choose a private host interface reachable by operators/nodes. The default bind
is loopback. Storage has no published ports; there are no Converge endpoints or
cloud credentials. Installation creates its own CA, tenant, operator token,
database and explicit local account. Sign in using the supplied credentials;
there is no default password or public signup. Bootstrap password staging is
removed after successful installation.

Distribute state/pki/ca.crt using your normal trust workflow, or place a customer
HTTPS reverse proxy in front. The proxy must verify the installation CA upstream,
preserve Origin/Host and stream SSE/gRPC without buffering. Set --origin to the
real browser origin. Override --agent-manager-url / --agent-ingest-url if node
endpoints differ. Do not bypass certificate warnings or TLS verification.

```sh
python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py status --directory /srv/haproxy-manager
```

Keep the installation directory mode 0700. Its generated runtime.json contains
credentials: do not print it in tickets or commit it. Inspect local service logs
with the generated Compose file:

```sh
docker compose --project-directory /srv/haproxy-manager --env-file /dev/null \
  -f /srv/haproxy-manager/runtime.json logs --tail=100 manager ingest
```

Failed child commands also save a private last-error.log inside the installation.
Treat it as potentially sensitive diagnostic material; do not publish it.

Additional administrator/viewer accounts use the existing
[host account commands](self-hosted-console.md#create-and-recover-accounts),
using this profile's tools Compose service for privileged commands. Teams/MFA
are deferred. Enroll existing proxies from the standalone console using each
node's own metrics/runtime/DPA paths; node control/config/TLS policies remain
explicit. Keep identity state separate from software. Never mount a Docker
socket into an agent.

## Upgrade, rollback and interruption

For the first candidate containing [distribution packaging](third-party.md),
download and authenticate its matching `selfhostctl.py` and `agentctl.py` pair
before upgrading. Older tools reject the additional signed legal/source files.
Keep using the new authenticated host tools for rollback to an older manager;
they retain support for previous release manifests. The legal files are kept with
the verified release snapshot and included in encrypted backup/restore.

Authenticate the new bundle and take a verified off-host backup first. Keep
using the authenticated current host installer for upgrades, software rollback
and recovery, including after rolling back the manager executable:

```sh
python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py upgrade \
  --directory /srv/haproxy-manager --release ./release-vMAJOR.MINOR.PATCH --version vMAJOR.MINOR.PATCH
python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py rollback \
  --directory /srv/haproxy-manager --release ./release-vPREVIOUS.FLEET.TAG --version vPREVIOUS.FLEET.TAG
```

The tool verifies the image's actual executable before process changes,
uses a separate database owner for migrations, and checks UI/fleet/stores/version.
A failed candidate restores the previous software. HAProxy keeps serving its
loaded configuration during management restarts. Agent upgrades remain separate.

Storage image pins must remain unchanged. The installer supports the
specific additive schema-19-to-20 migration for live observations; other schema
changes are refused pending separate qualification. Software rollback leaves
schema 20 in place and runs the previous manager/ingestion binaries. It never
rewinds database data, renewed certificates, node keys or journals, and selects
only the retained previous release. The rc.5 installer predates this migration
and rejects schema 20. Host tools before rc.7 also omit connector settings;
use rc.7 or later when managing a linked installation.
See [ADR 0054](adr/0054-haproxy-additive-observation-upgrade.md).

```sh
python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py recover --directory /srv/haproxy-manager
```

A durable pending marker blocks conflicting changes after interruption.
Recovery resumes bootstrap or restores the retained previous software. Missing
or changed installation keys fail closed; restore them rather than reinitialize.

## Encrypted and authenticated backup

Provision recovery keys outside installation/node state. age encrypts to an
X25519 recipient; a separate Ed25519 signature authenticates the creator.
Hold private keys in your recovery secret store and distribute trusted public
keys independently:

```sh
age-keygen -o /secure/recovery/manager.agekey
age-keygen -y /secure/recovery/manager.agekey
openssl genpkey -algorithm ED25519 -out /secure/recovery/backup-signing.key
openssl pkey -in /secure/recovery/backup-signing.key -pubout -out /secure/recovery/backup-signing.pub
chmod 600 /secure/recovery/manager.agekey /secure/recovery/backup-signing.key

python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py backup \
  --directory /srv/haproxy-manager --backup /backup-staging/manager-2026-10-27.age \
  --recipient YOUR_PUBLIC_AGE_RECIPIENT \
  --signing-key /secure/recovery/backup-signing.key
```

The checkpoint briefly stops management/ingestion and telemetry writers,
captures pg_dump, installation PKI/credentials, local accounts, release records,
optional connector state and cold metrics/log volumes, then resumes services.
Private temporary plaintext staging is removed; secure host-storage erasure
is not promised. HAProxy nodes are outside this stack and keep serving.

Keep **both** manager-2026-10-27.age and manager-2026-10-27.age.proof. The proof is
written last; its absence means an incomplete backup. Copy the pair off-host to
access-controlled storage, apply retention through your backup system and
rehearse restores. Schedule this host command externally; automatic remote upload,
retention and scheduling are not part of this tool. Archive limits are 32 GiB and
200,000 entries. Retain exact images and release-publisher trust keys too.

### Node state and keys

The manager cannot recover node keys it never held. On each node host, stop
all identity/configuration writers in a coordinated configuration-maintenance
window. Stop the agent and DPA/other writers. If DPA shares a HAProxy container,
withdraw/drain that node before stopping it while a healthy peer serves.

Private node-backup.json example:

```json
{
  "state_dir": "/srv/haproxy-node/agent",
  "containers": ["my-agent", "my-haproxy-dpa"],
  "paths": {
    "haproxy": "/srv/haproxy-node/persistent-haproxy",
    "policy": "/srv/haproxy-node/policy",
    "tls": "/srv/haproxy-node/tls"
  }
}
```

Include actual paths for agent journals/artifacts, config, DPA assets/credentials,
TLS and ACME account/order state. Exclude runtime sockets, which are recreated.
Every listed writer container must exist and be stopped. For native/systemd
nodes, use the equivalent stopped-service backup workflow; this helper currently
checks Docker writers.

```sh
python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py node-backup \
  --node-config /srv/node-backup.json --backup /backup-staging/node-a.age \
  --recipient YOUR_PUBLIC_AGE_RECIPIENT \
  --signing-key /secure/recovery/backup-signing.key
```

Resume, health-check and rejoin the node afterward. Keep node payload/proof pairs
with the matching manager checkpoint. Node keys never pass through the manager.

## Cold restore

Fence original management/agents before activating replacements. Restore always
uses a new directory and fresh volumes:

```sh
python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py restore \
  --directory /srv/haproxy-manager-restored \
  --backup /recovery/manager-2026-10-27.age \
  --identity-file /secure/recovery/manager.agekey \
  --backup-public-key /secure/recovery/backup-signing.pub
```

Offline publisher signatures also need the independent release --public-key.
The tool first copies bounded ciphertext into private staging, then verifies
and decrypts that same snapshot. Allow up to 32 GiB for the ciphertext copy in
addition to decrypted staging. It verifies the backup signature, complete
decryption, manifest/archives
and retained signed release. It preserves tenant, PKI, users and history, then
leaves services stopped and bound to loopback. Override --origin / --manager-port /
--ingest-port as necessary, retaining a hostname covered by the existing cert.
Privately update installation.json with the intended private bind/agent endpoint
URLs before activation. Optional outbound linking stays disabled by default.

```sh
python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py activate \
  --directory /srv/haproxy-manager-restored --source-fenced
python3 -I ./release-vMAJOR.MINOR.PATCH/selfhostctl.py node-restore \
  --directory /srv/restored-node-a --backup /recovery/node-a.age \
  --identity-file /secure/recovery/manager.agekey \
  --backup-public-key /secure/recovery/backup-signing.pub
```

Activation refuses a source manager/ingestion still running locally. Across
hosts, enforce fencing yourself before --source-fenced. Node restore writes
private files and starts nothing. Restore numeric owners as root, or use the
original service UID/GID. Point agents at the restored manager, omit enrollment
tokens and verify new metrics/discovery with their original IDs before rejoining.
Never run two copies of one node identity.

## Independent acceptance

Development/CI needs Go, pnpm, Docker and age:

```sh
make selfhost-acceptance HAPROXY_SELFHOST_ARGS='--work /tmp/haproxy-clean-install'
```

The runner builds signed **test fixtures**, installs copied release assets in
fresh storage, creates local accounts and two real HAProxy nodes, then exercises
upgrade/rollback, a broken candidate and encrypted cold recovery. It verifies
historic metrics, original node identities and TLS keys while two paths serve
over HTTP/HTTPS. It uses no Converge source, services, account, database or demo
state. Compilation is only fixture preparation, not customer installation.

Only randomized test projects are removed; latest.json retains sanitized
evidence. --keep leaves that private synthetic installation for inspection.
Linux CI runs the same scenario on its own runner. This qualifies a single-host
lifecycle; it does not certify production capacity or multi-host availability.

Run acceptance on a dedicated Linux Docker Engine with at least 4 GiB allocated
and no other running workloads. The runner refuses smaller/shared engines before
creating state. A separate Colima profile with --activate=false is one local
option; run the installer inside that VM so the normal Docker context stays
unchanged. Do not run this alongside development stacks in a 2-GiB VM.

Local qualification on 2026-10-27 ran the installer itself in a dedicated 4-GiB
Ubuntu 24.04 Linux aarch64 VM, with Python 3.12.3, Compose 5.1.4, age 1.2.1,
fresh storage and no Converge services. The final run served 332 continuous
HTTP/HTTPS requests without failures,
then verified 40 requests against newly started restored HAProxy processes using
the original trusted certificates and keys. Compatible upgrade, explicit rollback,
broken-start automatic rollback, source-fencing rejection, restored local login,
fresh node telemetry and historic metric points all passed. Releases were signed
disposable fixtures, not a published production tag.

An earlier shared 2-GiB development-VM trial exhausted memory and stopped the
development metrics store; it was restarted and both original nodes reported
fresh metrics again. Acceptance now checks memory and rejects engines with other
running workloads before creating a lab. The final dedicated-VM run above avoids
that resource competition.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
