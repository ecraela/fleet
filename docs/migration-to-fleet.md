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

The private RC.14 installations continue using their authenticated artifacts and
original publisher. Changing a source dependency is not a deployment upgrade.
Keep their existing host tools, release directories, identity/state directories,
keys and backup/restore procedures.

New Fleet host tools verify `ecraela/fleet` manifests, exact image digests and
the tag-specific Sigstore workflow identity. They do not automatically trust
artifacts from the predecessor repository. Existing host tools likewise do not
automatically trust the new publisher.

A cross-publisher upgrade must be qualified separately before a running
installation moves. Do not edit `deployment.json`, overwrite a saved trust root,
disable signature checks or re-enroll nodes to force that transition. Software
rollback must retain verification of the original release publisher.

## Public releases

The initial public snapshot provided source and a reproducible local demo.
Fleet binary and container releases remain candidates until their matching
published-asset qualification passes. The new
release workflow targets `ghcr.io/ecraela/fleet` and refuses publication from a
different repository or to a private package.

Before the first published release, configure the new Fleet GHCR package for
public distribution. Never change the visibility of the predecessor package.
Qualify the new artifacts on clean Linux, including anonymous downloads/pulls,
local login, node enrollment, HTTP/HTTPS traffic and encrypted cold restore.
Subsequent Fleet releases also require published upgrade/rollback qualification
against an earlier Fleet release. The older private qualification records do not
substitute for these checks.

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet is
not affiliated with or endorsed by HAProxy Technologies.
