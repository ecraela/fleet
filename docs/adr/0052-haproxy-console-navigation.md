# 0052: Console navigation

Status: accepted, 2026-10-28.

## Decision

Use resource pages with stable URLs in both the standalone and Converge consoles.
The main sections are Overview, Applications, Infrastructure, Activity and
Settings. Operators select a cluster before entering its workspace. Application,
node and cluster detail pages keep their owning cluster in the route.

Editing forms open in drawers. Destructive application actions use confirmation
dialogs. Table search, pagination and history filters remain in the URL so
back/forward navigation restores the view.

The authenticated workspace owns the API client and bounded caches. Navigation
keeps validated observations visible while refreshing them; freshness checks
still mark old evidence as unknown. Logout, tenant changes and disposal clear
the client caches. Pages stop their polling and streams when unmounted.

Standalone serves known console routes from Go. Missing assets and API paths
remain distinct from the SPA fallback. Both consoles share view behavior, with
adapters for local authentication and Converge's MFA/DNS integration. Standalone
does not import the private Converge workspace.

## Consequences

Operators can bookmark resources and investigate traffic without reloading the
whole fleet. Routing and cache tests must cover both route bases, deep links,
cluster ownership, tenant isolation and disposal. UI navigation does not change
permissions, node-local policy or the separate DNS review flow.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
