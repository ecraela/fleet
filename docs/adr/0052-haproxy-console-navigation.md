# 0052: HAProxy console navigation

Status: Accepted

Date: 2026-10-28

## Context

Fleet monitoring, application routing, infrastructure controls and installation
settings outgrew a single page. Opening a resource should provide a stable link
without discarding the manager connection or recently loaded observations.

## Decision

Both consoles use five sections:

- Overview: selected fleet health, traffic history and active incidents.
- Applications: searchable application inventory and application detail pages.
- Infrastructure: clusters, nodes, topology, listeners, TLS and node operations.
- Activity: incident history, deployments and node maintenance history.
- Settings: installation connection, agent versions and recovery guidance.

Applications have Overview, Backends, Traffic and Deployments views. Nodes have
Metrics, Configuration, Runtime and Maintenance views. Clusters have Topology,
Listeners, Automatic TLS and Rollouts views. Forms and request details open in
drawers. Application Deployments contains its complete version history and
restore actions; application Disable and Delete use destructive confirmation
dialogs. The application header’s overflow menu contains its actions.

Each view has a URL. Standalone serves these routes from Go, including direct
links and refreshes. Converge uses the same paths under its organization-scoped
HAProxy route. Cluster, history range and inventory search remain in the URL.
Browser back/forward navigation restores that state.

The HAProxy client lives for the authenticated workspace, not for each page.
Its bounded memory caches preserve validated observations and application data
across navigation. A page stops its polling/streams on unmount; returning shows
cached data while refreshing it. Freshness checks still turn old evidence into
unknown state. Logout, tenant changes and disposal clear the client caches.

Shared view source is maintained in both repositories with explicit local-auth
and Converge MFA/DNS adapters. Standalone uses public React Router and the local
React Aria component snapshot. It does not import the Converge workspace.

## Cluster entry (2026-11-02)

The landing page now lists clusters before opening their five sections. A row
shows observed health, reporting-node counts and recorded 15-minute bandwidth
and HTTP-response sparklines. Search and pagination keep history reads limited
to ten visible clusters, twelve active nodes per cluster and four concurrent
requests. Missing observations and incomplete inventory remain explicit.

Opening a cluster scopes the sections to it; the Clusters breadcrumb restores
the list’s search and page. Existing resource URLs select their owning cluster
ahead of query parameters. Installation settings remain accessible without a
cluster. Both consoles use this flow, with their existing authentication and
permission boundaries unchanged.

Infrastructure uses persistent Nodes, Topology, Listeners, Automatic TLS and
Rollouts tabs. The Infrastructure entry opens Nodes; existing cluster URLs
select the corresponding tab and keep the same section heading. Returning to
Nodes retains its query and pagination. Individual nodes keep their own detail
pages. Manual refresh and node revocation sit in the page’s overflow menu;
automatic observations and the existing revocation confirmation remain in place.

## Consequences

Operators can bookmark resources and inspect traffic without an ever-growing
fleet page. The standalone server must serve known console routes while keeping
API paths and missing assets distinct. Tests cover both route bases, deep links,
tenant-scoped caches and disposal. Permissions, agent policy, verification and
audit continue to gate HAProxy writes. DNS retains its separate review flow.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
