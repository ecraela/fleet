# Production qualification

This checklist prepares the next release after `v0.1.0-rc.25`. It does not mark
an unpublished candidate or an installed fleet as qualified. Record exact source,
signed artifacts and results for the intended Ubuntu 24.04 amd64/arm64 deployment.
Other host, HAProxy and networking combinations require their own evidence.

## Release candidate

- [ ] Select an exact candidate tag and source commit containing native updater
      fix `19d4d09bcb96d331a242b7ff67a38c1d935ffd88` and the qualification changes.
- [ ] Run `make web-install`, then `make format check test build` and `make vuln`.
- [ ] Require source CI, two-node acceptance/hardening, standalone recovery and
      the **Host-owned managed manager and agent upgrades under traffic** job to pass
      for that same source commit. A skipped or cancelled job is not a pass.
- [ ] Add the exact candidate's reviewed operator notes and compatibility entry
      under `deploy/release-notes/` and `deploy/release-compatibility/`. Start with
      the tested RC.25-to-candidate transition; widen the peer range only after
      exercising each additional transition. Include the management interruption
      and the old native updater bridge below in the signed notes.
- [ ] Publish through the signed release workflow. Authenticate the candidate's
      Sigstore workflow identity, manifest and exact image digest before executing
      any downloaded tool. Preserve the authenticated baseline for recovery.
- [ ] Run the **Published Fleet release qualification** workflow from the
      candidate's source, with baseline `v0.1.0-rc.25` and the exact candidate tag.
      Both Ubuntu architecture jobs must pass. Retain the downloaded evidence
      outside the short-lived Actions artifact window.

The published qualification uses real signed assets for clean installation,
direct upgrade/rollback, native managed upgrade/rollback/re-upgrade, TLS traffic
and encrypted cold recovery. Native managed changes use local administrator
sessions and CSRF, the installed host updater service, exact operation receipts,
fresh telemetry, preserved identity and signed updater hashes. If either signed
release lacks managed-update capability, its report says `unsupported`; that
result does not qualify the managed path.

The source managed-upgrade CI job exercises manager/ingest and a Docker agent
canary under two-node traffic. These per-host scenarios complement the coordinator
contracts; the installation-wide UI and linked Converge path still need the
operator witness below.

## RC.25 native updater bridge

RC.25's installed native updater caches the executable and helper but omits the
packaging files required by the native installer. Downloading a fixed target
does not update an already installed updater. An existing failed or incomplete
cache needs operator inspection before retry; do not delete identity, journals
or pending markers to clear it.

Before the first managed update, use the independently authenticated candidate
host tool to stage the complete signed target bundle on each affected native
host, with its existing profile and publisher. The target must include `LICENSE`,
`THIRD_PARTY_NOTICES.txt`, `dependencies.json`, `source.tar.gz` and `source.json`.
Preserve a complete authenticated rollback bundle as well. Qualify this bridge
on disposable hosts before applying it to an installation.

For the disposable native acceptance, the candidate tool enables the updater
from the signed baseline bundle and stages the candidate. The test verifies the
service initially uses the baseline's exact updater bytes. It then requests
upgrade, rollback and re-upgrade through the browser API and verifies that each
target's signed updater activates. The native scenario runs before changing the
manager, so the baseline's signed peer range remains valid during agent rollback.

The native fixture reads systemd's configured restart interval and waits between
deliberate managed transitions. It retains the real restart-rate protection;
qualification never resets failed state or disables that guard to force a pass.

## Installation witness

- [ ] Record the installed manager, ingest, relay and agent versions and updater
      readiness. Confirm the signed peer ranges admit every intermediate state.
- [ ] Retrieve and verify an encrypted off-host backup. Restore it on a fenced,
      isolated host using the original identities and keys; retain the measured
      recovery time and the exact artifact digests.
- [ ] In the standalone console, request one installation-wide release. Verify
      ordered manager/ingest and agent steps, live traffic, fresh post-change health,
      operation receipts and the observed final versions. Exercise the qualified
      rollback ordering separately.
- [ ] Repeat the request through the production-shaped Converge connector and
      relay. Confirm reconnect, local login independence and the same audit trail.
- [ ] Witness a stopped updater or failed host check pausing the installation.
      Repair the cause, plan a fresh request and verify remaining hosts converge.
- [ ] Complete a new 48-hour soak of the intended installed release, using the
      pilot soak runner. Preserve its final passed result, traffic distribution,
      reporting continuity and original identity checks. An older release's soak
      does not qualify this candidate.
- [ ] Verify off-host backup retrieval and operator alert delivery on the actual
      installation. Record the tested recovery and support expectations.

## Evidence record

Keep a sanitized record with the candidate tag, source commit, signed manifest
digest, image digest, CI/qualification run URLs, architecture, baseline version,
transition results, traffic failures, restore duration, soak interval and result,
and the operator's decision. Mark each missing receipt `pending`. Store private
host identities, credentials, cookies, profiles and diagnostic bodies separately;
they never belong in this repository or the public release notes.

## Local preparation check — 16 November 2026

The preparation working tree is based on source `19d4d09`; it is not a published
release. `make format`, `make check`, `make test` and `make build` passed. The
tests included the Go race suite on disposable PostgreSQL 18, 127 console tests
and all 131 Python tests, including encrypted-backup cases with the pinned
`age` 1.2.1 tools available.

The managed manager/ingest and Docker-agent smoke passed in a separate local
Linux VM with four CPUs and 6 GiB configured memory. Both signed fixture upgrades
and rollbacks reported success; 90 synthetic HTTP/HTTPS requests completed with
zero serving failures. The disposable containers and identities were cleaned up.
This checks the preparation tooling, not public release artifacts, the new native
managed scenario, installation-wide browser behavior or production capacity.
Those release and installation gates remain pending above.
