# Moving to Ecraela Fleet

Fleet's canonical source repository is [`ecraela/fleet`](https://github.com/ecraela/fleet).
It starts with one signed snapshot of the reviewed private manager source and
the public-project changes. The earlier Git history, release tags, CI output and
published images remain in their original private locations.

## Source and build changes

The Go module is `github.com/ecraela/fleet`. The manager and agent executable
names remain `haproxy-manager` and `haproxy-agent`. Protocol package names, API
paths, environment variables, cookies, metrics, persistent directories and state
formats retain their existing identifiers. Renaming the project does not
re-enroll a node or replace any manager, node or serving key.

Converge consumes the new repository at an exact Git commit. It still uses the
independent Go service through its API and optional outbound connector. Fleet's
local login, storage and embedded console work without Converge.

## Existing installations

The private release-candidate installations continue using their authenticated artifacts and
original publisher. Changing a source dependency is not a deployment upgrade.
Keep their existing host tools, release directories, identity/state directories,
keys and backup/restore procedures.

New Fleet host tools verify `ecraela/fleet` manifests, exact image digests and
the tag-specific Sigstore workflow identity. They do not automatically trust
artifacts from the predecessor repository. Existing host tools likewise do not
automatically trust the new publisher.

The Fleet host tools now have an explicit `migrate` action for this boundary.
It verifies the installed predecessor bundle and a newer Fleet bundle under
their respective publishers, then changes the saved trust root only after the
new software passes health checks. Rollback verifies the retained predecessor
bundle under its original publisher. This source change is **not** a qualified
production migration or a published release. Do not edit `deployment.json`,
overwrite a saved trust root, disable signature checks or re-enroll nodes.

## Migration procedure after release qualification

First take the manager and node backups described in the self-hosted and agent
runbooks. Record the current manager and each agent version/publisher, the
protocol and the HAProxy version. Keep the original signed release directory,
including `release.json` and its signature bundle, and obtain the exact new
Fleet release directory. The Fleet version must be newer than the installed
private version. A rebuilt private artifact or a same-version Fleet artifact
will be refused.

**Authenticate the target installer before executing it.** Verify the target
`release.json` with its tag-specific Sigstore identity, or with a separately
provisioned Ed25519 public key for an offline release. Check the installer file
hash against that authenticated manifest as described in
[agent releases](agent-releases.md#trust-a-release). A downloaded public key next
to an untrusted bundle is not a trust anchor. Keep the signer key out of the
release directory and all hosts being upgraded.

For a self-hosted manager, use the authenticated Fleet host tool:

```bash
python3 -I /path/to/fleet-release/selfhostctl.py migrate \
  --directory /srv/haproxy-manager \
  --source-release /path/to/installed-private-release \
  --release /path/to/fleet-release --version vMAJOR.MINOR.PATCH
python3 -I /path/to/fleet-release/selfhostctl.py status \
  --directory /srv/haproxy-manager
```

For each agent, one at a time:

```bash
python3 -I /path/to/fleet-release/agentctl.py migrate \
  --config /path/to/agentctl.json \
  --source-release /path/to/installed-private-release \
  --release /path/to/fleet-release --version vMAJOR.MINOR.PATCH
python3 -I /path/to/fleet-release/agentctl.py status \
  --config /path/to/agentctl.json
```

Add `--public-key /path/to/fleet-publisher.pub` when the Fleet release uses an
independently provisioned Ed25519 signer. Add
`--source-public-key /path/to/private-publisher.pub` if the installed private
release used an offline signer. The tool compares the predecessor manifest and
trust root with installed state, and checks the old executable before changing
processes. Its status shows the
current and previous publisher without exposing keys.

If the process is interrupted, run the same Fleet host tool's `recover` action;
it restores the old current release and its trust. For a deliberate software
rollback after a successful migration, pass the exact old signed bundle to
`rollback` with its original version and, if needed, its original `--public-key`.
Rollback is limited to the retained previous release. Restore of lost identity
or database state is a separate backup procedure; rollback never rewinds it.

The disposable qualification must establish manager/agent/relay ordering,
connector compatibility, and HAProxy service continuity before any pilot use.
Migrate the manager and then one agent at a time only after that evidence is
recorded. Switch Converge's platform relay image in its own rollout and verify
connector reconnection and tenant boundaries. The private publisher remains
available for authenticated rollback until the pilot is stable.

## Public releases

The initial public snapshot provided source and a reproducible local demo.
Fleet binary and container releases need matching published-asset
qualification. The new
release workflow targets `ghcr.io/ecraela/fleet` and refuses publication from a
different repository or to a private package.

Before the first published release, configure the new Fleet GHCR package for
public distribution. Never change the visibility of the predecessor package.
Qualify the new artifacts on clean Linux, including anonymous downloads/pulls,
local login, node enrollment, HTTP/HTTPS traffic and encrypted cold restore.
Subsequent Fleet releases also require published upgrade/rollback qualification
against an earlier Fleet release. The older private qualification records do not
substitute for these checks.

The current signed release workflow uses GitHub Actions to build/push the image
and issue a tag-specific Sigstore certificate. With Actions unavailable, a
local build cannot create that certificate. The independently provisioned
Ed25519 mode is a distinct publisher. Before publishing a new release through
that route, qualify the build and signature on both architectures, verify image
and native executable hashes, distribute the public key independently and
confirm GHCR and release-asset publication are available. No offline-signed
candidate is installed merely because its checksum matches a file beside it.
See the [non-Actions release runbook](release-without-actions.md) for the
operator procedure and its current console-enrollment limit.

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet is
not affiliated with or endorsed by HAProxy Technologies.
