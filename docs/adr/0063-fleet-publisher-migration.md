# ADR 0063: Explicit migration between signed release publishers

Status: proposed for issue #8, 8 November 2026. Production qualification and
release publication remain open.

## Context

The public Fleet source and the private HAProxy Manager predecessor have
different release publishers. The current host tools pin one publisher in each
installation and reject an ordinary upgrade across that boundary. A source
submodule update does not change a running manager, ingest service or agent.

## Decision

Add a host-owned `migrate` action to the Fleet agent and self-hosted manager
installers. It is accepted only when the installed manifest names the private
predecessor. The operator supplies both the installed predecessor's signed
bundle and a newer Fleet signed bundle. The tool authenticates each exact
manifest under its own publisher, matches the predecessor manifest and trust
root to installed state, checks the retained predecessor executable and image,
and checks protocol, state schema, target image digest and the self-hosted storage
profile before changing a process. Version must increase
so publisher-specific artifacts never collide in version-keyed release folders.

The target Fleet installer is part of the authenticated target bundle. Private
release tools are verified as artifacts but never executed for migration. The
agent keeps its identity and private keys; the manager keeps its installation,
database, connector and account state. Their host tools retain the previous
image or executable and persist a pending operation before switching software.

The installed trust root changes only after target health and identity checks
succeed. The state records the previous manifest and previous trust root for an
explicit rollback. That rollback reauthenticates the exact retained previous
release under its original publisher. A failed candidate automatically returns
to the old release without changing trust. Recovery after an interrupted
migration activates the old current release and retains its trust root.
Ordinary `upgrade` remains within one publisher; a private manifest is never
accepted as a normal Fleet install.

The Converge relay is a separate, platform-owned service. Its image transition
needs a Converge PR and connector compatibility check. It does not receive
customer manager or node keys. The Converge source submodule commit is a build
pin, not a release publisher or an operator-selected runtime version.

## Release availability

The existing Fleet release workflow builds the image and signs the manifest
under a GitHub Actions OIDC identity. When Actions is unavailable, a local
builder cannot claim that identity. The installer also supports an independently
provisioned Ed25519 publisher, but its public key is a *different* trust root.
An offline-signed release requires a reviewed build, exact image/binary match,
protected signing key, independent public-key delivery and explicit installation
trust transition. Registry and release-asset publication must be tested before
the pilot. No release is qualified merely because this migration code exists.
The current `main` ruleset requires six Actions status checks and has no bypass
actor. With runners unavailable, a repository policy decision is required
before this change can be merged and tagged; local results do not satisfy those
status contexts.

## Qualification gates

- Authenticate a real predecessor bundle and a real Fleet bundle, including
  executable and image hashes on both supported Linux architectures.
- Exercise manager and each agent migration, automatic recovery and explicit
  rollback on disposable hosts. Preserve identity, certificates, state and
  HAProxy traffic while manager/agent processes restart.
- Check connector v1, management and telemetry through the cloud relay; switch
  the relay to a Fleet image as an independently verified platform rollout.
- Repeat on the two-node pilot after backup and documented recovery. Do not
  retire private release publication until that evidence is recorded.
