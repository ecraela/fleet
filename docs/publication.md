# Source publication and releases

Fleet's canonical public source is [`ecraela/fleet`](https://github.com/ecraela/fleet).
It starts from a reviewed snapshot of the private manager source, with a new Go
module, product name and release publisher. See [ADR 0062](adr/0062-fleet-public-source.md)
and [migration](migration-to-fleet.md).

## Initial source snapshot

The export contains tracked source, documentation, the selected AGPL-3.0-only
license and third-party notices. It excludes Git history, local identities,
credentials, build output and operator evidence. The prior repository, release
assets, Actions history and container package retain their private locations.

Publication checks cover the exact new tracked tree and its initial commit,
including dependency/license inventory, default-rule secret scanning, source
builds, unit/storage tests and real existing-node onboarding. Retained audit
receipts stay outside this repository. The preceding private RC.14 qualification
is useful provenance; it does not qualify a newly built Fleet artifact.

GitHub controls must protect `main` and release tags, prevent force-pushes and
deletion, and require the intended checks. Fork workflows require approval from
outside contributors. Workflow tokens default to read-only, cannot approve pull
requests, and current action uses are pinned to commits. Public repository
visibility is separate from package visibility.

## Signed releases

No Fleet binary or container release is advertised as qualified yet. Release
manifests and host tools identify `ecraela/fleet`; the container namespace is
`ghcr.io/ecraela/fleet`. Configure that new package for public distribution before
publishing releases. The workflow must refuse a different repository, a private
release repository or a private package. Never change the predecessor package's
visibility to bootstrap the new namespace.

Each release requires exact tag, publisher, digest and source verification, clean
Linux installation, local login, node enrollment, HTTP/HTTPS traffic and encrypted
cold restore. Verify anonymous asset downloads and image pulls. Subsequent
Fleet releases also require published upgrade/rollback qualification. Existing
installations need a separately qualified cross-publisher transition; source
publication does not rotate their saved trust roots.

## Contributions and reporting

The selected source license is [AGPL-3.0-only](../LICENSE), with
[optional commercial licensing](../LICENSING.md). Contributors retain copyright.
Additional rights must be arranged in writing before external contributions
enter the commercial offering; see [contributing](../CONTRIBUTING.md).

Report vulnerabilities through [SECURITY.md](../SECURITY.md), using Ecraela's
existing private reporting contact. Public source does not authorize testing
customer or Ecraela production infrastructure.

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet is
not affiliated with or endorsed by HAProxy Technologies.
