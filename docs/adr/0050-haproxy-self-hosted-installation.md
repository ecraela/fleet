# ADR 0050: Repeatable self-hosted installation and recovery

Status: accepted for the first Linux/Docker profile (2026-10-27).

The standalone console and connector pilots reused a development installation.
A customer installation needs its own identity, storage and local account,
without a Converge checkout or service.

## Installation and upgrades

Distribute the host-owned Python 3.11+ selfhostctl.py in the existing signed
release bundle. The manifest binds this tool, its agentctl.py dependency,
selfhost.json storage profile, Linux binaries and exact manager image digest.
The profile pins PostgreSQL, VictoriaMetrics and VictoriaLogs by repository
digest. Customer installs have no source builds, mutable tags, unsigned mode
or implicit latest-version lookup.

The first profile uses a local Linux amd64/arm64 Docker Engine and Compose v2+.
Linux/systemd agents retain their existing installer; native manager/systemd
packaging is deferred. The manager serves its embedded console and local login.
The host tool never runs in the manager or grants it Docker authority.

Initialize a new private directory, installation CA, operator credential, tenant
and explicit local administrator password. Never overwrite an installation or
regenerate missing keys. Go containers are unprivileged with read-only roots.
Storage has an internal network and no published ports. Separate database owner
and runtime roles prevent runtime access from disabling immutable-audit triggers.
Ingestion gets no CA signing key or operator token. Converge is optional and
disabled by default. HTTPS/secure cookies are the production default; only an
explicit localhost development option enables HTTP. Teams and MFA are deferred.

Authenticate a selected exact release and check the manager executable copied
from its image against the signed native artifact/build identity before stopping
anything. Retain previous software and images, serialize operations, and persist
a pending marker before process changes. Readiness checks the database-backed
fleet, console assets, both telemetry stores and expected version. A failed
candidate restores the previous software and verifies it. Interrupted operations
require recover.

The first compatible-upgrade contract requires unchanged database schema and
storage image pins. It never downgrades storage or rewinds node identities,
command journals, TLS or ACME state. Storage/schema changes need a separately
qualified migration. Rollback selects only the retained previous signed release.
ADR 0054 qualifies the specific additive schema-19-to-20 transition while
preserving the same storage image pins and retaining schema 20 on software rollback.

## Backup and restore

Host backup stops management/ingestion and metrics/log writers, dumps PostgreSQL
logically, copies private installation/release state and checkpoints cold
telemetry volumes. Resume writers in finally even after copy/encryption failure.
Serving HAProxy nodes are outside this stack.

Encrypt to an independently held age X25519 recipient. Sign the ciphertext
digest/size using a separate Ed25519 backup key and publish its proof last.
AEAD integrity alone does not authenticate who created an archive. Restore
authenticates the independent signature and complete decryption before
extraction. Incomplete, corrupt, linked, special-file, oversized or unsigned
backups are refused. This profile bounds archives to 32 GiB and 200,000 entries.

The manager never collects node private keys. Run node-backup on each node host,
with explicit stopped identity/configuration writers and paths for journals,
HAProxy/DPA state, TLS and ACME material. Coordinate node and manager checkpoints
in a configuration-maintenance window. A DB-only backup cannot recover node
keys. The current helper checks Docker writer containers; native/systemd node
backup orchestration remains a documented stopped-service workflow.

Restore into a new directory, randomized Compose project and fresh volumes.
Preserve the tenant, PKI, accounts, connector state and node identities/keys.
The restored stack stays stopped and bound to loopback. Activation requires
source fencing and refuses a source manager/ingestion still running locally.
Across hosts the operator enforces fencing. Node restore only writes private
files and never starts a copied identity. Linking stays disabled unless separately
configured. Never run original and restored agents for one identity together.

## Qualification and boundaries

The independent runner creates signed fixture releases, fresh storage, a local
administrator and two real HAProxy nodes/agents with HTTP/HTTPS traffic. It tests
compatible upgrade/rollback, failed-candidate recovery, encrypted signed backup,
cold fresh-storage restore, login, historic metrics and preserved identities/TLS
keys. It imports no Converge demo helpers or database. Linux CI runs the same
scenario. Fixture signing does not publish a supported release.

This single-host profile does not promise management HA, public DNS/ACME,
provider networking, automatic remote backup transfer/retention or production
capacity. Operators store payload/proof pairs off-host, retain independent keys
and exact images, schedule backups externally and rehearse restores. See the
[installation runbook](../self-hosted-installation.md).

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
