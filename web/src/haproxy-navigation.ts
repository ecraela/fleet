export const haproxySections = [
  { id: "overview", label: "Overview" },
  { id: "applications", label: "Applications" },
  { id: "infrastructure", label: "Infrastructure" },
  { id: "activity", label: "Activity" },
  { id: "settings", label: "Settings" },
] as const;
export type HAProxySection = (typeof haproxySections)[number]["id"];
export const applicationTabs = [
  "overview",
  "backends",
  "traffic",
  "geography",
  "rules",
  "deployments",
] as const;
export type ApplicationTab = (typeof applicationTabs)[number];
export const nodeTabs = [
  "metrics",
  "configuration",
  "runtime",
  "maintenance",
] as const;
export type NodeTab = (typeof nodeTabs)[number];
export const clusterTabs = [
  "topology",
  "listeners",
  "tls",
  "rollouts",
] as const;
export type ClusterTab = (typeof clusterTabs)[number];
export type HAProxyRoute =
  | { section: HAProxySection; kind: "list" }
  | {
      section: "applications";
      kind: "application";
      clusterId: string;
      id: string;
      tab: ApplicationTab;
    }
  | { section: "infrastructure"; kind: "node"; id: string; tab: NodeTab }
  | { section: "infrastructure"; kind: "cluster"; id: string; tab: ClusterTab }
  | { section: "overview"; kind: "not-found" };

export function readHAProxyRoute(pathname: string, base = ""): HAProxyRoute {
  const prefix = base.replace(/\/$/, "");
  if (prefix && pathname !== prefix && !pathname.startsWith(prefix + "/"))
    return { section: "overview", kind: "not-found" };
  let parts: string[];
  try {
    parts = pathname
      .slice(prefix.length)
      .replace(/^\/+|\/+$/g, "")
      .split("/")
      .filter(Boolean)
      .map(decodeURIComponent);
  } catch {
    return { section: "overview", kind: "not-found" };
  }
  if (parts.some((p) => !/^[a-zA-Z0-9_-]{1,128}$/.test(p)))
    return { section: "overview", kind: "not-found" };
  if (!parts.length || (!prefix && parts.length === 1 && parts[0] === "login"))
    return { section: "overview", kind: "list" };
  if (parts.length === 1 && haproxySections.some((s) => s.id === parts[0]))
    return { section: parts[0] as HAProxySection, kind: "list" };
  if (
    parts[0] === "applications" &&
    (parts.length === 3 || parts.length === 4)
  ) {
    const tab = parts[3] ?? "overview";
    if (applicationTabs.includes(tab as ApplicationTab))
      return {
        section: "applications",
        kind: "application",
        clusterId: parts[1]!,
        id: parts[2]!,
        tab: tab as ApplicationTab,
      };
  }
  if (
    parts[0] === "infrastructure" &&
    (parts.length === 3 || parts.length === 4)
  ) {
    if (
      parts[1] === "nodes" &&
      nodeTabs.includes((parts[3] ?? "metrics") as NodeTab)
    )
      return {
        section: "infrastructure",
        kind: "node",
        id: parts[2]!,
        tab: (parts[3] ?? "metrics") as NodeTab,
      };
    if (
      parts[1] === "clusters" &&
      clusterTabs.includes((parts[3] ?? "topology") as ClusterTab)
    )
      return {
        section: "infrastructure",
        kind: "cluster",
        id: parts[2]!,
        tab: (parts[3] ?? "topology") as ClusterTab,
      };
  }
  return { section: "overview", kind: "not-found" };
}
export function haproxyPath(
  base: string,
  parts: readonly string[],
  search = "",
): string {
  return `${base.replace(/\/$/, "")}/${parts.map(encodeURIComponent).join("/")}${search}`;
}
export function historyRange(value: string | null) {
  return value === "1h" || value === "6h" || value === "24h" ? value : "15m";
}

/** Resource URLs own their cluster; a stale query must never switch that context. */
export function selectedHAProxyCluster(
  route: HAProxyRoute,
  selection: string | null,
  nodes: readonly { id: string; cluster_id: string }[],
): string | null {
  if (route.kind === "application") return route.clusterId;
  if (route.kind === "cluster") return route.id;
  if (route.kind === "node")
    return nodes.find((node) => node.id === route.id)?.cluster_id ?? null;
  if (route.kind === "not-found") return null;
  return selection && selection !== "all" ? selection : null;
}
