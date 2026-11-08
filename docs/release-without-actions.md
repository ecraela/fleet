# Signed Fleet release when GitHub Actions is unavailable

This is the operator procedure for an independently provisioned Ed25519
publisher. It does **not** produce the GitHub Actions Sigstore identity used by
the normal release workflow. A candidate remains unqualified until the
published assets, both architectures and upgrade/rollback have been exercised.
Keep the signing key off the build host, repository, containers and customer
hosts. Deliver its public key to each installation through an independent
trusted channel; a key downloaded beside a release bundle is not sufficient.

Fleet's [active `main` ruleset](https://github.com/ecraela/fleet/rules/26627443)
currently requires six GitHub Actions status
checks. Local results cannot satisfy those contexts. Before merging or tagging
a non-Actions release, the repository owner must explicitly restore runner
access or approve a replacement branch policy and qualification evidence. Keep
the signed-commit and immutable-tag protections. This procedure grants no
exception to the current ruleset.

## Prepare a candidate

Use a dedicated Linux builder with the pinned Go, Node, pnpm, Docker Buildx,
PostgreSQL, Python and backup tools. Check out the reviewed `main` commit that
will be tagged. Set `version` to a new exact release tag and `revision` to that
commit's full SHA. The working tree must be clean. Run the same local checks
as the release workflow, including the database-backed race suite and
dependency audit:

```bash
make web-install
make format check test build vuln web-audit
git status --porcelain
```

The final command must print nothing. Build and push a **staging** multi-arch
image to the public Fleet GHCR package. Confirm GHCR write access first; the
organization's Packages budget may prevent publishing. Use an operator-owned
registry credential outside the repository. The staging tag is never a release
pin:

```bash
mkdir -p .build.local/offline-release
docker buildx build --platform linux/amd64,linux/arm64 --push \
  --tag "ghcr.io/ecraela/fleet:staging-$revision" \
  --build-arg "VERSION=$version" --build-arg "REVISION=$revision" \
  --label "org.opencontainers.image.source=https://github.com/ecraela/fleet" \
  --label "org.opencontainers.image.version=$version" \
  --label "org.opencontainers.image.revision=$revision" \
  --metadata-file .build.local/offline-release/image.json .
```

Read `containerimage.digest` from the Buildx metadata, require a `sha256:`
digest, and use `ghcr.io/ecraela/fleet@<that digest>` as the `--image` argument
to `scripts/release.py`. Give it the exact version, revision and a new output
directory. Then run `scripts/verify_release_images.py` against that directory.
It pulls both platforms and compares the agent, manager, notices and source
files inside the image with the release files. Refuse a mismatch; do not sign
the manifest until these checks pass.

Transfer **only** `release.json` to the offline signer. Sign those exact bytes
with the independently held Ed25519 key:

```bash
openssl pkeyutl -sign -inkey /secure/fleet-publisher.key -rawin \
  -in release.json -out release.sig
```

Return `release.sig` to the candidate directory. On the build host, verify it
with the independently provisioned public key and then run both host-tool
verifiers before publishing:

```bash
openssl pkeyutl -verify -pubin -inkey /secure/fleet-publisher.pub -rawin \
  -in release.json -sigfile release.sig
python3 -I scripts/agentctl.py verify --release /path/to/candidate \
  --version "$version" --public-key /secure/fleet-publisher.pub
python3 -I scripts/selfhostctl.py verify --release /path/to/candidate \
  --version "$version" --public-key /secure/fleet-publisher.pub
```

Before running a host tool copied from the release assets, independently
verify `release.json` and compare that tool's SHA-256 with the signed
`artifacts` entry. A valid signature is required before trusting any path or
checksum named by the manifest.

## Publish and qualify

Tag the exact reviewed revision and publish the release files, including
`release.sig`, as immutable GitHub release assets. Promote the verified staging
image digest to the version tag and confirm the final tag still resolves to
that digest. Do not claim the image has a GitHub Actions Sigstore signature.
Check anonymous asset download and image pull from another host, then verify
the manifest with the separately delivered public key. Retain the build log,
source revision, image digest, public-key fingerprint, test results and
qualification record outside customer state.

The current console-generated enrollment commands assume the GitHub Actions
Sigstore publisher. For this release route, use the authenticated host tools
with `--public-key` until the console supports an explicitly provisioned
offline publisher. The cross-publisher migration steps are in
[Moving to Ecraela Fleet](migration-to-fleet.md). A production pilot migration
also requires the real predecessor bundle and a tested rollback.
