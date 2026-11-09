import {
  validGeographyTraffic,
  type HAProxyGeographyTraffic,
} from "./haproxy-geography-api";
import {
  validTrafficPolicy,
  validTrafficCapabilities,
  validTrafficCounts,
  type TrafficPolicy,
  type TrafficCapabilities,
  type TrafficRuleCounts,
} from "./haproxy-traffic-model";
import { validNodeSetup, type HAProxyNodeSetup } from "./haproxy-setup";
import {
  requestQuery,
  validRequestTraffic,
  validRequestPage,
  type HAProxyRequestFilters,
  type HAProxyRequestTraffic,
  type HAProxyRequestPage,
} from "./haproxy-requests-api";
import { ApiError } from "./api";
import {
  validIncidentPage,
  type HAProxyIncidentPage,
} from "./haproxy-incidents-api";
export type {
  HAProxyIncident,
  HAProxyIncidentEvent,
  HAProxyIncidentPage,
} from "./haproxy-incidents-api";
import {
  mergeLiveHistory,
  snapshotLive,
  type HAProxyClusterLive,
  type HAProxyLiveNode,
} from "./haproxy-live";

const base = "/api/haproxy/v1";

export interface HAProxyConfigurationServer {
  name: string;
  address: string;
  port: number;
  weight: number;
  maintenance: boolean;
  forward_incoming_port?: boolean;
}
export interface HAProxyConfigurationBackend {
  name: string;
  algorithm: "roundrobin" | "leastconn" | "first";
  servers: HAProxyConfigurationServer[];
}
export interface HAProxyConfigurationDocument {
  backends: HAProxyConfigurationBackend[];
  applications?: HAProxyApplication[];
  listeners?: HAProxyHTTPSListener[];
}
export interface HAProxyListenerBinding {
  name: string;
  address: string;
  port: number;
}
export interface HAProxyHTTPSListener extends HAProxyListenerBinding {
  certificate_ref: string;
  certificate_sha256: string;
  node_certificates?: Record<string, string>;
}
export interface HAProxyTLSCertificate {
  reference: string;
  sha256: string;
  dns_names: string[];
  not_before_ms: number;
  not_after_ms: number;
  error_code: string;
}
export interface HAProxyACMEChallenge {
  hostname: string;
  target: string;
}
export interface HAProxyACMEGrant {
  issuer: string;
  listener: string;
  environment: "staging" | "production";
  challenges: HAProxyACMEChallenge[];
  policy_digest: string;
}
export interface HAProxyACMEOption {
  issuer: string;
  environment: "staging" | "production";
  binding: HAProxyListenerBinding;
  challenges: HAProxyACMEChallenge[];
}
export interface HAProxyACMEJob {
  id: string;
  plan_id: string;
  generation: number;
  node_id: string;
  issuer: string;
  listener: string;
  hostnames: string[];
  policy_digest: string;
  reference: string;
  status: "pending" | "issuing" | "ready" | "failed";
  error_code: string;
  certificate?: HAProxyTLSCertificate;
  attempts: number;
  next_try_ms: number;
  deadline_ms: number;
}
export interface HAProxyACMEPlan {
  id: string;
  organization_id: string;
  cluster_id: string;
  version: number;
  enabled: boolean;
  issuer: string;
  environment: "staging" | "production";
  binding: HAProxyListenerBinding;
  hostnames: string[];
  challenges: HAProxyACMEChallenge[];
  generation: number;
  status: "issuing" | "ready" | "deploying" | "active" | "error" | "disabled";
  error_code: string;
  jobs: HAProxyACMEJob[];
  history: Array<{
    generation: number;
    reference: string;
    rollout_id: string;
    status: HAProxyRollout["status"];
    completed_at_ms: number;
    certificates: Record<string, HAProxyTLSCertificate>;
  }>;
  renew_at_ms: number;
  next_run_ms: number;
  rollout_id?: string;
}
export interface HAProxyACMEView {
  plan: HAProxyACMEPlan | null;
  options: HAProxyACMEOption[];
}
export interface HAProxyACMEInput {
  request_id: string;
  expected_version: number;
  action: "enable" | "renew" | "retry" | "disable";
  issuer: string;
  binding: HAProxyListenerBinding;
}
export interface HAProxyListenerRevision {
  id: string;
  organization_id: string;
  cluster_id: string;
  number: number;
  request_id: string;
  listeners: HAProxyHTTPSListener[];
  rollout_id: string;
  rollback_of?: string;
  status: HAProxyRollout["status"];
  created_at: string;
}
export interface HAProxyListenerInput {
  request_id: string;
  expected_revision: number;
  listeners: HAProxyHTTPSListener[];
  nodes: HAProxyRolloutTarget[];
  entrypoint: HAProxyRolloutTarget & { binding_id: string };
}
export interface HAProxyApplication {
  traffic_policy?: TrafficPolicy;
  id: string;
  name: string;
  listener: string;
  hostname: string;
  path_prefix: string;
  algorithm: HAProxyConfigurationBackend["algorithm"];
  servers: HAProxyConfigurationServer[];
  health_path: string;
  disabled?: boolean;
  request_telemetry?: boolean;
  verification?: { http: boolean; https: boolean; entrypoint?: boolean };
}
export interface HAProxyApplicationRevision {
  id: string;
  organization_id: string;
  cluster_id: string;
  application_id: string;
  number: number;
  request_id: string;
  spec: HAProxyApplication;
  rollout_id: string;
  rollback_of?: string;
  created_at: string;
  status: HAProxyRollout["status"];
  deleted?: boolean;
}
export type HAProxyApplicationAction =
  "disable" | "enable" | "delete" | "restore";
export type HAProxyApplicationLifecycleInput = Omit<
  HAProxyApplicationInput,
  "spec"
> & { action: HAProxyApplicationAction };
export interface HAProxyApplicationInput {
  request_id: string;
  expected_revision: number;
  spec: HAProxyApplication;
  nodes: HAProxyRolloutTarget[];
  entrypoint: HAProxyRolloutTarget & { binding_id: string };
}
export interface HAProxyApplicationProbe {
  scope?: "node" | "entrypoint";
  policy_digest?: string;
  artifact_id?: string;
  generation?: string;
  application_id: string;
  spec_digest: string;
  name: string;
  scheme: "http" | "https";
  path: string;
  expected_status: number;
  status_code: number;
  observed_at_ms: number;
  duration_ms: number;
  error_code: string;
  tls_verified: boolean;
  certificate_sha256: string;
  certificate_not_after_ms: number;
}
export interface HAProxyApplicationProbeGrant {
  name: string;
  scope: "node" | "entrypoint";
  listener: string;
  hostname: string;
  path_prefix: string;
  path: string;
  scheme: "http" | "https";
  expected_status: number;
  policy_digest: string;
}
export interface HAProxyRolloutApplicationCheck extends HAProxyApplicationProbeGrant {
  application_id: string;
  spec_digest: string;
}
export interface HAProxyApplicationVerification {
  phase:
    | "before_rejoin"
    | "after_rejoin"
    | "after_node_restore"
    | "after_cluster_restore";
  status: "waiting" | "passed" | "failed";
  started_at_ms: number;
  error_code: string;
  checks: Array<{
    name: string;
    scope?: "node" | "entrypoint";
    application_id: string;
    scheme: "http" | "https";
    path: string;
    expected_status: number;
    status_code: number;
    tls_verified: boolean;
    observed_at_ms: number;
    passes: number;
    error_code: string;
  }>;
}
export function validApplicationProbeGrants(
  value: unknown,
): value is HAProxyApplicationProbeGrant[] {
  return (
    Array.isArray(value) &&
    value.length <= 32 &&
    new Set(value.map((g) => g?.name)).size === value.length &&
    value.every(
      (g) =>
        g &&
        typeof g.name === "string" &&
        /^[-A-Za-z0-9_.:]{1,128}$/.test(g.name) &&
        ["node", "entrypoint"].includes(g.scope) &&
        typeof g.listener === "string" &&
        /^[-A-Za-z0-9_.:]{1,128}$/.test(g.listener) &&
        typeof g.hostname === "string" &&
        /^[a-z0-9.-]{1,253}$/.test(g.hostname) &&
        typeof g.path_prefix === "string" &&
        /^\/[A-Za-z0-9/._~-]{0,127}$/.test(g.path_prefix) &&
        typeof g.path === "string" &&
        /^\/[A-Za-z0-9/._~-]{0,127}$/.test(g.path) &&
        g.path.startsWith(g.path_prefix) &&
        ["http", "https"].includes(g.scheme) &&
        Number.isInteger(g.expected_status) &&
        g.expected_status >= 200 &&
        g.expected_status <= 399 &&
        typeof g.policy_digest === "string" &&
        /^[a-f0-9]{64}$/.test(g.policy_digest),
    )
  );
}
export function validApplicationVerification(
  value: unknown,
): value is HAProxyApplicationVerification {
  if (!value || typeof value !== "object") return false;
  const v = value as HAProxyApplicationVerification;
  return (
    [
      "before_rejoin",
      "after_rejoin",
      "after_node_restore",
      "after_cluster_restore",
    ].includes(v.phase) &&
    ["waiting", "passed", "failed"].includes(v.status) &&
    Number.isSafeInteger(v.started_at_ms) &&
    v.started_at_ms > 0 &&
    typeof v.error_code === "string" &&
    v.error_code.length <= 64 &&
    Array.isArray(v.checks) &&
    v.checks.length > 0 &&
    v.checks.length <= 32 &&
    new Set(v.checks.map((c) => c?.name)).size === v.checks.length &&
    v.checks.every(
      (c) =>
        c &&
        acmeUUID(c.application_id) &&
        typeof c.name === "string" &&
        /^[-A-Za-z0-9_.:]{1,128}$/.test(c.name) &&
        (c.scope === undefined || ["node", "entrypoint"].includes(c.scope)) &&
        ["http", "https"].includes(c.scheme) &&
        typeof c.path === "string" &&
        /^\/[A-Za-z0-9/._~-]{0,127}$/.test(c.path) &&
        Number.isInteger(c.expected_status) &&
        c.expected_status >= 200 &&
        c.expected_status <= 399 &&
        Number.isInteger(c.status_code) &&
        c.status_code >= 0 &&
        c.status_code <= 599 &&
        typeof c.tls_verified === "boolean" &&
        Number.isSafeInteger(c.observed_at_ms) &&
        c.observed_at_ms >= 0 &&
        Number.isInteger(c.passes) &&
        c.passes >= 0 &&
        c.passes <= 2 &&
        typeof c.error_code === "string" &&
        c.error_code.length <= 64,
    )
  );
}
export interface HAProxyApplicationHealth {
  application_id: string;
  configuration: "loaded" | "updating" | "drifted" | "unknown";
  reachability: "reachable" | "unhealthy" | "unknown" | "disabled" | "deleted";
  nodes: Array<{
    node_id: string;
    name: string;
    fresh: boolean;
    observed_at_ms: number;
    loaded: boolean;
    revision: number;
    spec_digest: string;
    servers: HAProxyBackendServer[];
    probes: HAProxyApplicationProbe[];
  }>;
}
export function validApplicationHealth(
  value: unknown,
): value is HAProxyApplicationHealth[] {
  const uuid = (v: unknown) =>
    typeof v === "string" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
  const integer = (v: unknown, min: number, max: number) =>
    typeof v === "number" && Number.isSafeInteger(v) && v >= min && v <= max;
  return (
    Array.isArray(value) &&
    value.length <= 256 &&
    new Set(value.map((h) => h?.application_id)).size === value.length &&
    value.every(
      (h: HAProxyApplicationHealth) =>
        h &&
        uuid(h.application_id) &&
        ["loaded", "updating", "drifted", "unknown"].includes(
          h.configuration,
        ) &&
        ["reachable", "unhealthy", "unknown", "disabled", "deleted"].includes(
          h.reachability,
        ) &&
        Array.isArray(h.nodes) &&
        h.nodes.length <= 12 &&
        new Set(h.nodes.map((n) => n?.node_id)).size === h.nodes.length &&
        h.nodes.every(
          (n) =>
            n &&
            uuid(n.node_id) &&
            typeof n.name === "string" &&
            n.name.length <= 255 &&
            typeof n.fresh === "boolean" &&
            integer(n.observed_at_ms, 0, Number.MAX_SAFE_INTEGER) &&
            typeof n.loaded === "boolean" &&
            integer(n.revision, 0, 64) &&
            typeof n.spec_digest === "string" &&
            (n.spec_digest === "" || /^[a-f0-9]{64}$/.test(n.spec_digest)) &&
            Array.isArray(n.servers) &&
            n.servers.length <= 16 &&
            n.servers.every(
              (s) =>
                s &&
                typeof s.name === "string" &&
                s.name.length <= 128 &&
                typeof s.health === "string" &&
                s.health.length <= 64 &&
                typeof s.state === "string" &&
                ["ready", "drain", "maint"].includes(s.state) &&
                integer(s.weight, 0, 256),
            ) &&
            Array.isArray(n.probes) &&
            n.probes.length <= 32 &&
            new Set(n.probes.map((p) => p?.name)).size === n.probes.length &&
            n.probes.every(
              (p) =>
                p &&
                p.application_id === h.application_id &&
                typeof p.name === "string" &&
                /^[-A-Za-z0-9_.:]{1,128}$/.test(p.name) &&
                typeof p.spec_digest === "string" &&
                /^[a-f0-9]{64}$/.test(p.spec_digest) &&
                ["http", "https"].includes(p.scheme) &&
                typeof p.path === "string" &&
                /^\/[A-Za-z0-9/._~-]*$/.test(p.path) &&
                p.path.length <= 128 &&
                integer(p.expected_status, 200, 399) &&
                integer(p.status_code, 0, 599) &&
                integer(p.observed_at_ms, 1, Number.MAX_SAFE_INTEGER) &&
                integer(p.duration_ms, 0, 5000) &&
                [
                  "",
                  "connection_failed",
                  "tls_failed",
                  "response_status",
                  "response_mismatch",
                  "response_too_large",
                  "deadline",
                  "route_conflict",
                ].includes(p.error_code) &&
                typeof p.tls_verified === "boolean" &&
                typeof p.certificate_sha256 === "string" &&
                (p.certificate_sha256 === "" ||
                  /^[a-f0-9]{64}$/.test(p.certificate_sha256)) &&
                integer(p.certificate_not_after_ms, 0, Number.MAX_SAFE_INTEGER),
            ),
        ),
    )
  );
}
export interface HAProxyConfigurationState {
  traffic_rules?: TrafficCapabilities;
  request_logs_allowed?: boolean;
  node_id?: string;
  acme_grants?: HAProxyACMEGrant[];
  application_control_version?: number;
  listener_control_version?: number;
  listener_bindings?: HAProxyListenerBinding[];
  tls_certificates?: HAProxyTLSCertificate[];
  tls_listeners?: Array<{ name: string; sha256: string; loaded: boolean }>;
  version: number;
  artifact_id: string;
  document?: HAProxyConfigurationDocument;
  observed_at_ms: number;
  control_allowed: boolean;
  error_code: string;
}
export interface HAProxyConfigurationRevision {
  id: string;
  organization_id: string;
  node_id: string;
  number: number;
  parent_id?: string;
  rollback_of?: string;
  operation_id?: string;
  artifact_id?: string;
  document: HAProxyConfigurationDocument;
  source: "discovered" | "edit" | "rollback";
  status: "observed" | "queued" | "running" | "applied" | "failed" | "unknown";
  error_code: string;
  created_at: string;
  completed_at?: string;
}
export interface HAProxyConfigurationView {
  control: HAProxyNodeDiscovery["control"];
  connected: boolean;
  fresh: boolean;
  configuration_enabled: boolean;
  versions: HAProxyConfigurationRevision[];
}
export interface HAProxyConfigurationInput {
  request_id: string;
  expected_generation: string;
  expected_artifact_id: string;
  expected_version: number;
  document?: HAProxyConfigurationDocument;
}

export interface HAProxyRolloutTarget {
  node_id: string;
  expected_generation: string;
  expected_artifact_id: string;
  expected_version: number;
}
export interface HAProxyRolloutInput {
  request_id: string;
  document: HAProxyConfigurationDocument;
  nodes: HAProxyRolloutTarget[];
  entrypoint?: HAProxyRolloutTarget & { binding_id: string };
  drain_timeout_seconds?: number;
}
export interface HAProxyRollout {
  id: string;
  organization_id: string;
  cluster_id: string;
  request_id: string;
  document: HAProxyConfigurationDocument;
  status:
    | "running"
    | "rolling_back"
    | "succeeded"
    | "rolled_back"
    | "rollback_failed";
  error_code: string;
  index: number;
  created_at: string;
  updated_at: string;
  completed_at?: string;
  entrypoint?: HAProxyRolloutTarget & {
    binding_id: string;
    cluster_id: string;
    topology: HAProxyTopology;
    baseline: HAProxyConfigurationDocument;
    owned_artifact_id: string;
    owned_version: number;
  };
  drain_timeout_seconds?: number;
  nodes: Array<
    HAProxyRolloutTarget & {
      name: string;
      baseline_revision_id: string;
      apply_operation_id?: string;
      rollback_operation_id?: string;
      status: string;
      error_code: string;
      health_since_ms: number;
      last_health_ms: number;
      application_checks?: HAProxyRolloutApplicationCheck[];
      rollback_application_checks?: HAProxyRolloutApplicationCheck[];
      application_verification?: HAProxyApplicationVerification;
      rollback_application_verification?: HAProxyApplicationVerification;
      maintenance?: HAProxyMaintenance;
      rollback_maintenance?: HAProxyMaintenance;
      recovery_maintenance_id?: string;
    }
  >;
}
export interface HAProxyBackendServer {
  backend: string;
  name: string;
  address: string;
  state: "ready" | "drain" | "maint";
  weight: number;
  initial_weight: number;
  active_connections: number;
  health: string;
  check_status?: string;
  admin_flags: number;
}
export interface HAProxyDiscovery {
  setup?: HAProxyNodeSetup;
  agent?: HAProxyAgentBuild;
  application_probe_grants?: HAProxyApplicationProbeGrant[];
  configuration?: HAProxyConfigurationState;
  version: string;
  process_id: string;
  started_at: number;
  config_fingerprint: string;
  generation: string;
  control_allowed: boolean;
  observed_at_ms: number;
  error_code: string;
  config_files: number;
  proxies: Array<{
    name: string;
    kind: "frontend" | "backend";
    mode: string;
    algorithm: string;
    status: string;
    active_connections?: number;
  }>;
  servers: HAProxyBackendServer[];
}
export interface HAProxyOperation {
  id: string;
  request_id: string;
  node_id: string;
  organization_id: string;
  kind: "drain" | "restore" | "weight" | "configuration" | "maintenance";
  backend: string;
  server: string;
  weight: number;
  expected_generation: string;
  expected_state: "ready" | "drain" | "maint";
  expected_weight: number;
  status: "queued" | "running" | "succeeded" | "failed" | "unknown";
  error_code: string;
  created_at: string;
  deadline: string;
  completed_at: string | null;
}
export interface HAProxyNodeDiscovery {
  manager_build?: HAProxyAgentBuild;
  control: {
    node_id: string;
    current_revision_id?: string;
    configuration_history_limited?: boolean;
    active_rollout_id?: string;
    maintenance_id?: string;
    maintenance_blocked?: boolean;
    connected_at: string;
    last_seen: string;
    discovery: HAProxyDiscovery | null;
  };
  connected: boolean;
  fresh: boolean;
  management_enabled: boolean;
  operations: HAProxyOperation[];
}
export type HAProxyOperationInput = Pick<
  HAProxyOperation,
  | "request_id"
  | "backend"
  | "server"
  | "weight"
  | "expected_generation"
  | "expected_state"
  | "expected_weight"
> & { kind: Exclude<HAProxyOperation["kind"], "configuration"> };

export interface HAProxyAgentSetup {
  release_version?: string;
  release_url?: string;
  image: string;
  manager_url: string;
  ingest_url: string;
  network: string;
  metrics_url: string;
  ca_pem: string;
}

export interface HAProxyAgentBuild {
  version: string;
  revision: string;
  platform: string;
  protocol: number;
  executable_sha256: string;
}

export interface HAProxyEnrollmentStatus {
  state: "pending" | "connected" | "expired" | "revoked";
  node?: HAProxyEnrolledNode;
}

export type HAProxyEnrolledNode = Pick<
  HAProxyNode,
  "id" | "organization_id" | "cluster_id" | "name" | "created_at" | "revoked_at"
>;

export interface HAProxyTopology {
  control?: {
    node_id: string;
    backend: string;
    servers: Array<{ node_id: string; server: string }>;
  };
  entrypoint: string;
  strategy: "roundrobin" | "leastconn" | "active-passive";
  frontend: string;
  backend: string;
  nodes: Array<{ node_id: string; address: string }>;
}

export interface HAProxyMaintenance {
  id: string;
  organization_id: string;
  cluster_id: string;
  node_id: string;
  controller_node_id: string;
  controller_cluster_id: string;
  binding_id: string;
  backend: string;
  server: string;
  frontend: string;
  status:
    | "withdrawing"
    | "draining"
    | "maintenance"
    | "checking_rejoin"
    | "rejoining"
    | "attention"
    | "completed"
    | "failed";
  step: string;
  error_code: string;
  connections?: number;
  health_since_ms?: number;
  last_health_ms?: number;
  created_at: string;
  updated_at: string;
  completed_at?: string;
  deadline: string;
}
export interface HAProxyMaintenanceView {
  frontend: string;
  node_id: string;
  cluster_id: string;
  binding_id?: string;
  reason_code: string;
  entrypoint: string;
  backend: string;
  server: string;
  controller_node_id?: string;
  controller_name: string;
  target: HAProxyNodeDiscovery["control"];
  controller: HAProxyNodeDiscovery["control"];
  maintenance?: HAProxyMaintenance;
}
export interface HAProxyMaintenanceInput {
  request_id: string;
  action: "withdraw" | "rejoin";
  maintenance_id?: string;
  binding_id: string;
  expected_generation: string;
  expected_entrypoint_generation: string;
  expected_artifact_id: string;
  expected_version: number;
  drain_timeout_seconds: number;
}

export interface HAProxyCluster {
  topology?: HAProxyTopology;
  id: string;
  organization_id: string;
  name: string;
  created_at: string;
}

export interface HAProxyNode {
  agent?: HAProxyAgentBuild;
  agent_fresh?: boolean;
  haproxy_version?: string;
  id: string;
  organization_id: string;
  cluster_id: string;
  name: string;
  created_at: string;
  revoked_at?: string | null;
  telemetry_fresh: boolean;
  scrape_ok: boolean;
  observed_at: string | null;
  metrics_available: boolean;
}

export interface HAProxyFleet {
  manager_build?: HAProxyAgentBuild;
  clusters: HAProxyCluster[];
  nodes: HAProxyNode[];
  next_cursor: string | null;
  partial: boolean;
}

export interface HAProxySoftwareRelease {
  version: string;
  revision: string;
  manifest_sha256: string;
  publisher: string;
  platforms: string[];
  compatibility: {
    manager: { min: string; max: string };
    agent: { min: string; max: string };
  };
}

export interface HAProxySoftwareUpdater {
  current_version: string;
  ingest_version?: string;
  previous_version?: string;
  ready: boolean;
  last_seen: string;
}

export interface HAProxySoftwareUpdate {
  id: string;
  organization_id: string;
  cluster_id: string;
  node_id: string;
  request_id: string;
  action: "upgrade" | "rollback";
  target_version: string;
  expected_version: string;
  status:
    | "queued"
    | "preparing"
    | "activating"
    | "verifying"
    | "needs_attention"
    | "succeeded"
    | "rolled_back"
    | "cancelled"
    | "recovered";
  error_code?: string;
  created_at: string;
  updated_at: string;
}

export interface HAProxySoftwareView {
  updates: HAProxySoftwareUpdate[];
  updater: HAProxySoftwareUpdater | null;
  observed: {
    build: HAProxyAgentBuild | null;
    connected: boolean;
    last_seen: string;
  } | null;
}

export interface HAProxySoftwarePlan {
  release: HAProxySoftwareRelease;
  current: HAProxyAgentBuild;
  manager: HAProxyAgentBuild;
  action: "upgrade" | "rollback";
  eligible: boolean;
  reason?: string;
  traffic_impact: string;
  management_impact: string;
  updater?: HAProxySoftwareUpdater;
}

export interface HAProxyInstallationSoftwareUpdate {
  id: string;
  organization_id: string;
  request_id: string;
  action: "upgrade" | "rollback";
  target_version: string;
  expected_version: string;
  status: HAProxySoftwareUpdate["status"];
  error_code?: string;
  created_at: string;
  updated_at: string;
}

export interface HAProxyInstallationSoftwareView {
  updates: HAProxyInstallationSoftwareUpdate[];
  updater: HAProxySoftwareUpdater | null;
  observed: HAProxyAgentBuild;
}

export interface HAProxyInstallationSoftwarePlan {
  release: HAProxySoftwareRelease;
  current: HAProxyAgentBuild;
  action: "upgrade" | "rollback";
  eligible: boolean;
  reason?: string;
  traffic_impact: string;
  management_impact: string;
  agent_count: number;
  updater?: HAProxySoftwareUpdater;
}

export interface MetricSeries {
  metric: Record<string, string>;
  value: [number, string];
}

export interface HAProxySnapshot {
  traffic_frontend?: string;
  node_id: string;
  observed_at: string | null;
  fresh: boolean;
  scrape_ok: boolean;
  data: { resultType: string; result: MetricSeries[] };
}

export type HAProxyHistoryRange = "15m" | "1h" | "6h" | "24h";

export interface HAProxyHistory {
  node_id: string;
  range: HAProxyHistoryRange;
  start: number;
  end: number;
  step_seconds: number;
  series: Array<{
    metric: { series: string; code?: string; proxy?: string };
    values: Array<[number, number]>;
  }>;
}

export interface HAProxyObservations {
  current: HAProxySnapshot | null;
  previous: HAProxySnapshot | null;
}

function remember<K, V>(cache: Map<K, V>, key: K, value: V): void {
  cache.delete(key);
  cache.set(key, value);
  if (cache.size > 12) cache.delete(cache.keys().next().value!);
}

interface BrowserSession {
  organization_id: string;
  user_id: string;
  capabilities: string[];
  csrf_token: string;
  expires_at: string;
}

export class HAProxyError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "HAProxyError";
  }
}

export function haproxyErrorMessage(error: unknown): string {
  if (error instanceof HAProxyError) return error.message;
  if (error instanceof ApiError) return error.message;
  return "HAProxy could not connect. Check your connection and retry.";
}

const clients = new Set<HAProxyClient>();
let logoutContext: { csrf: string; organization: string } | null = null;

export function stopHAProxyClients(): void {
  for (const client of clients) client.dispose();
  logoutContext = null;
}

/** Called before signing out locally; credentials never leave memory. */
export async function logoutHAProxy(): Promise<void> {
  for (const client of clients) client.dispose();
  let context = logoutContext;
  logoutContext = null;
  try {
    if (!context) {
      const response = await fetch(`${base}/local/session`, {
        credentials: "same-origin",
        signal: AbortSignal.timeout(2000),
      });
      if (!response.ok) return;
      const session = (await response.json()) as BrowserSession;
      if (
        typeof session.csrf_token !== "string" ||
        typeof session.organization_id !== "string"
      )
        return;
      context = {
        csrf: session.csrf_token,
        organization: session.organization_id,
      };
    }
    await fetch(`${base}/local/session`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: {
        "X-CSRF-Token": context.csrf,
        "X-Organization-ID": context.organization,
        "X-HAProxy-Session": "local",
      },
      signal: AbortSignal.timeout(2000),
    });
  } catch {
    // Local sign-out must still complete when the manager is unreachable.
  }
}

/** One organization and one mounted view own this client and its requests. */
export class HAProxyClient {
  readonly controller = new AbortController();
  private session: BrowserSession | null = null;
  private managing = false;
  private authentication: Promise<void> = Promise.resolve();
  private requestAnalytics = new Map<string, HAProxyRequestTraffic>();
  private requestPages = new Map<string, HAProxyRequestPage>();
  private geographyAnalytics = new Map<string, HAProxyGeographyTraffic>();
  cachedRequestTraffic(
    cluster: string,
    app: string,
    filters: HAProxyRequestFilters,
  ) {
    return this.requestAnalytics.get(
      `${cluster}/${app}?${requestQuery(filters)}`,
    );
  }
  cachedRequests(
    cluster: string,
    app: string,
    filters: HAProxyRequestFilters,
    end?: number,
    before?: string,
  ) {
    return this.requestPages.get(
      `${cluster}/${app}?${requestQuery(filters, end, before)}`,
    );
  }
  cachedGeography(
    cluster: string,
    app: string,
    filters: HAProxyRequestFilters,
  ) {
    return this.geographyAnalytics.get(
      `${cluster}/${app}?${requestQuery(filters)}`,
    );
  }
  private applicationRecords = new Map<string, HAProxyApplicationRevision[]>();
  private applicationChecks = new Map<string, HAProxyApplicationHealth[]>();
  cachedApplications(clusterId: string) {
    return this.applicationRecords.get(clusterId);
  }
  cachedApplicationHealth(clusterId: string) {
    return this.applicationChecks.get(clusterId);
  }
  private observations = new Map<string, HAProxyObservations>();
  private discoveries = new Map<string, HAProxyNodeDiscovery>();
  private configurations = new Map<string, HAProxyConfigurationView>();
  private maintenanceViews = new Map<string, HAProxyMaintenanceView>();
  private clusterMaintenance = new Map<string, HAProxyMaintenance[]>();
  private clusterRollouts = new Map<string, HAProxyRollout[]>();
  private histories = new Map<
    string,
    { data: HAProxyHistory; loadedAt: number }
  >();

  private clusteredNodes = new Map<string, number>();
  private liveFrames = new Map<string, HAProxyLiveNode[]>();
  private historyListeners = new Map<string, Set<() => void>>();

  subscribeHistory(nodeId: string, listener: () => void): () => void {
    const listeners = this.historyListeners.get(nodeId) ?? new Set();
    listeners.add(listener);
    this.historyListeners.set(nodeId, listeners);
    return () => {
      listeners.delete(listener);
      if (!listeners.size) this.historyListeners.delete(nodeId);
    };
  }

  private recordLiveNodes(nodes: HAProxyLiveNode[]): void {
    for (const node of nodes) {
      if (node.revoked) {
        this.liveFrames.delete(node.node_id);
        for (const key of this.histories.keys())
          if (key.startsWith(`${node.node_id}/`)) this.histories.delete(key);
        for (const listener of this.historyListeners.get(node.node_id) ?? [])
          listener();
        continue;
      }
      if (!node.observed_at || !node.fresh) continue;
      const frames = (this.liveFrames.get(node.node_id) ?? []).filter(
        (old) =>
          old.observed_at !== node.observed_at &&
          Date.parse(old.observed_at!) > Date.now() - 120_000,
      );
      frames.push(node);
      frames.sort(
        (a, b) => Date.parse(a.observed_at!) - Date.parse(b.observed_at!),
      );
      remember(this.liveFrames, node.node_id, frames.slice(-120));
      for (const [key, cached] of this.histories) {
        if (key.startsWith(`${node.node_id}/`))
          this.histories.set(key, {
            ...cached,
            data: mergeLiveHistory(cached.data, frames),
          });
      }
      for (const listener of this.historyListeners.get(node.node_id) ?? [])
        listener();
    }
  }

  constructor(readonly organizationId: string) {
    clients.add(this);
  }

  dispose(): void {
    this.controller.abort();
    this.session = null;
    this.requestAnalytics.clear();
    this.requestPages.clear();
    this.geographyAnalytics.clear();
    this.applicationRecords.clear();
    this.applicationChecks.clear();
    this.observations.clear();
    this.discoveries.clear();
    this.configurations.clear();
    this.clusterRollouts.clear();
    this.maintenanceViews.clear();
    this.clusterMaintenance.clear();
    this.histories.clear();
    this.liveFrames.clear();
    this.clusteredNodes.clear();
    this.historyListeners.clear();
    clients.delete(this);
  }

  async connect(): Promise<void> {
    // Reload the persisted local identity and permissions.
    await this.login(false);
  }

  async login(manage: boolean): Promise<void> {
    return this.queueAuthentication(() => this.performLogin(manage));
  }

  private queueAuthentication(operation: () => Promise<void>): Promise<void> {
    const pending = this.authentication.catch(() => {}).then(operation);
    this.authentication = pending;
    return pending;
  }

  private async performLogin(manage: boolean): Promise<void> {
    const session = await this.send<BrowserSession>("/local/session");
    this.acceptSession(session);
    if (manage && !session.capabilities.includes("haproxy.manage"))
      throw new HAProxyError(403, "Your account has read-only access.");
  }

  async refresh(): Promise<void> {
    await this.login(this.managing);
  }

  fleet(cursor?: string): Promise<HAProxyFleet> {
    return this.send(
      `/fleet${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`,
    );
  }

  async softwareReleases(): Promise<HAProxySoftwareRelease[]> {
    const result = await this.send<{ releases: HAProxySoftwareRelease[] }>(
      "/software/releases",
    );
    if (
      !Array.isArray(result.releases) ||
      result.releases.length > 50 ||
      result.releases.some(
        (r) =>
          !r ||
          typeof r.version !== "string" ||
          typeof r.publisher !== "string" ||
          !Array.isArray(r.platforms) ||
          !r.compatibility?.manager ||
          !r.compatibility?.agent,
      )
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid signed release catalog.",
      );
    return result.releases;
  }

  async installationSoftware(): Promise<HAProxyInstallationSoftwareView> {
    const result =
      await this.send<HAProxyInstallationSoftwareView>("/software/manager");
    if (
      !Array.isArray(result.updates) ||
      result.updates.length > 20 ||
      result.updates.some((u) => u.organization_id !== this.organizationId) ||
      typeof result.observed?.version !== "string"
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid manager software state.",
      );
    return result;
  }

  async installationSoftwarePlan(
    version: string,
    action: "upgrade" | "rollback",
  ): Promise<HAProxyInstallationSoftwarePlan> {
    const result = await this.send<{ plan: HAProxyInstallationSoftwarePlan }>(
      `/software/manager/plan?version=${encodeURIComponent(version)}&action=${action}`,
    );
    if (
      result.plan?.release?.version !== version ||
      result.plan.action !== action ||
      typeof result.plan.eligible !== "boolean" ||
      typeof result.plan.agent_count !== "number"
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid manager software plan.",
      );
    return result.plan;
  }

  async requestInstallationSoftware(input: {
    request_id: string;
    action: "upgrade" | "rollback";
    version: string;
    expected_version: string;
  }): Promise<HAProxyInstallationSoftwareUpdate> {
    const result = await this.send<{
      update: HAProxyInstallationSoftwareUpdate;
    }>("/software/manager", { method: "POST", body: JSON.stringify(input) });
    if (
      result.update?.organization_id !== this.organizationId ||
      result.update.request_id !== input.request_id ||
      result.update.target_version !== input.version
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid manager update receipt.",
      );
    return result.update;
  }

  async cancelInstallationSoftware(
    updateId: string,
  ): Promise<HAProxyInstallationSoftwareUpdate> {
    const result = await this.send<{
      update: HAProxyInstallationSoftwareUpdate;
    }>(`/software/manager/${encodeURIComponent(updateId)}/cancel`, {
      method: "POST",
      body: "{}",
    });
    if (result.update?.id !== updateId || result.update.status !== "cancelled")
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid manager cancellation receipt.",
      );
    return result.update;
  }

  async nodeSoftware(nodeId: string): Promise<HAProxySoftwareView> {
    const result = await this.send<HAProxySoftwareView>(
      `/nodes/${encodeURIComponent(nodeId)}/software`,
    );
    if (
      !Array.isArray(result.updates) ||
      result.updates.length > 20 ||
      result.updates.some(
        (u) =>
          u.node_id !== nodeId || u.organization_id !== this.organizationId,
      )
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid software update history.",
      );
    return result;
  }

  async softwarePlan(
    nodeId: string,
    version: string,
    action: "upgrade" | "rollback",
  ): Promise<HAProxySoftwarePlan> {
    const result = await this.send<{ plan: HAProxySoftwarePlan }>(
      `/nodes/${encodeURIComponent(nodeId)}/software/plan?version=${encodeURIComponent(version)}&action=${action}`,
    );
    if (
      result.plan?.release?.version !== version ||
      result.plan.action !== action ||
      typeof result.plan.eligible !== "boolean" ||
      typeof result.plan.traffic_impact !== "string"
    )
      throw new HAProxyError(502, "HAProxy returned an invalid software plan.");
    return result.plan;
  }

  async requestSoftware(
    nodeId: string,
    input: {
      request_id: string;
      action: "upgrade" | "rollback";
      version: string;
      expected_version: string;
    },
  ): Promise<HAProxySoftwareUpdate> {
    const result = await this.send<{ update: HAProxySoftwareUpdate }>(
      `/nodes/${encodeURIComponent(nodeId)}/software`,
      { method: "POST", body: JSON.stringify(input) },
    );
    if (
      result.update?.node_id !== nodeId ||
      result.update.organization_id !== this.organizationId ||
      result.update.request_id !== input.request_id ||
      result.update.target_version !== input.version
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid software update receipt.",
      );
    return result.update;
  }

  async cancelSoftware(
    nodeId: string,
    updateId: string,
  ): Promise<HAProxySoftwareUpdate> {
    const result = await this.send<{ update: HAProxySoftwareUpdate }>(
      `/nodes/${encodeURIComponent(nodeId)}/software/${encodeURIComponent(updateId)}/cancel`,
      { method: "POST", body: "{}" },
    );
    if (
      result.update?.id !== updateId ||
      result.update.node_id !== nodeId ||
      result.update.status !== "cancelled"
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid cancellation receipt.",
      );
    return result.update;
  }

  cachedObservations(nodeId: string): HAProxyObservations {
    return this.observations.get(nodeId) ?? { current: null, previous: null };
  }

  cachedDiscovery(nodeId: string): HAProxyNodeDiscovery | undefined {
    return this.discoveries.get(nodeId);
  }
  async discovery(
    nodeId: string,
    signal?: AbortSignal,
  ): Promise<HAProxyNodeDiscovery> {
    const result = await this.send<HAProxyNodeDiscovery>(
      `/nodes/${encodeURIComponent(nodeId)}/discovery`,
      signal ? { signal } : {},
    );
    if (
      result.control?.node_id !== nodeId ||
      !Array.isArray(result.operations) ||
      result.operations.length > 50 ||
      result.operations.some(
        (op) =>
          op.node_id !== nodeId || op.organization_id !== this.organizationId,
      ) ||
      typeof result.connected !== "boolean" ||
      typeof result.fresh !== "boolean" ||
      typeof result.management_enabled !== "boolean"
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid configuration discovery.",
      );
    const d = result.control.discovery;
    if (d?.setup !== undefined && !validNodeSetup(d.setup))
      throw new HAProxyError(
        502,
        "HAProxy returned invalid node setup diagnostics.",
      );
    if (
      d?.configuration &&
      (!validTLSInventory(d.configuration) ||
        (d.configuration.document &&
          !validConfigurationDocument(d.configuration.document)))
    )
      throw new HAProxyError(502, "HAProxy returned invalid TLS discovery.");
    if (
      d &&
      (!Array.isArray(d.proxies) ||
        d.proxies.length > 128 ||
        !Array.isArray(d.servers) ||
        d.servers.length > 256)
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid configuration discovery.",
      );
    remember(this.discoveries, nodeId, result);
    return result;
  }

  cachedMaintenance(nodeId: string): HAProxyMaintenanceView | undefined {
    return this.maintenanceViews.get(nodeId);
  }
  cachedClusterMaintenance(
    clusterId: string,
  ): HAProxyMaintenance[] | undefined {
    return this.clusterMaintenance.get(clusterId);
  }
  private acceptMaintenance(
    clusterId: string,
    value: HAProxyMaintenance,
  ): void {
    if (
      value?.organization_id !== this.organizationId ||
      value.cluster_id !== clusterId ||
      !value.id ||
      !value.node_id ||
      !value.controller_node_id ||
      value.controller_cluster_id === clusterId ||
      ![
        "withdrawing",
        "draining",
        "maintenance",
        "checking_rejoin",
        "rejoining",
        "attention",
        "completed",
        "failed",
      ].includes(value.status) ||
      (value.connections !== undefined &&
        (!Number.isInteger(value.connections) || value.connections < 0))
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid maintenance progress.",
      );
  }
  async maintenance(
    nodeId: string,
    signal?: AbortSignal,
  ): Promise<HAProxyMaintenanceView> {
    const result = await this.send<HAProxyMaintenanceView>(
      `/nodes/${encodeURIComponent(nodeId)}/maintenance`,
      signal ? { signal } : {},
    );
    if (
      result?.node_id !== nodeId ||
      result.target?.node_id !== nodeId ||
      !result.cluster_id ||
      typeof result.reason_code !== "string" ||
      (!result.reason_code &&
        (!result.controller_node_id ||
          result.controller?.node_id !== result.controller_node_id ||
          !result.binding_id))
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid maintenance discovery.",
      );
    if (result.maintenance) {
      this.acceptMaintenance(result.cluster_id, result.maintenance);
      if (result.maintenance.node_id !== nodeId)
        throw new HAProxyError(
          502,
          "HAProxy returned another node’s maintenance.",
        );
    }
    remember(this.maintenanceViews, nodeId, result);
    return result;
  }
  async maintenanceHistory(
    clusterId: string,
    signal?: AbortSignal,
  ): Promise<HAProxyMaintenance[]> {
    const result = await this.send<{ maintenance: HAProxyMaintenance[] }>(
      `/clusters/${encodeURIComponent(clusterId)}/maintenance`,
      signal ? { signal } : {},
    );
    if (!Array.isArray(result.maintenance) || result.maintenance.length > 20)
      throw new HAProxyError(
        502,
        "HAProxy returned invalid maintenance history.",
      );
    result.maintenance.forEach((value) =>
      this.acceptMaintenance(clusterId, value),
    );
    remember(this.clusterMaintenance, clusterId, result.maintenance);
    return result.maintenance;
  }
  async changeMaintenance(
    nodeId: string,
    clusterId: string,
    input: HAProxyMaintenanceInput,
  ): Promise<HAProxyMaintenance> {
    const result = await this.send<{ maintenance: HAProxyMaintenance }>(
      `/nodes/${encodeURIComponent(nodeId)}/maintenance`,
      { method: "POST", body: JSON.stringify(input) },
    );
    this.acceptMaintenance(clusterId, result.maintenance);
    if (
      result.maintenance.node_id !== nodeId ||
      result.maintenance.binding_id !== input.binding_id ||
      (input.maintenance_id && result.maintenance.id !== input.maintenance_id)
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid maintenance request identity.",
      );
    return result.maintenance;
  }

  cachedRollouts(clusterId: string): HAProxyRollout[] | undefined {
    return this.clusterRollouts.get(clusterId);
  }
  async listeners(
    clusterId: string,
    signal?: AbortSignal,
  ): Promise<HAProxyListenerRevision[]> {
    const result = await this.send<{ revisions: HAProxyListenerRevision[] }>(
      `/clusters/${encodeURIComponent(clusterId)}/listeners`,
      signal ? { signal } : {},
    );
    if (!Array.isArray(result.revisions) || result.revisions.length > 64)
      throw new HAProxyError(502, "HAProxy returned invalid listener history.");
    result.revisions.forEach((r) => this.acceptListener(clusterId, r));
    return result.revisions;
  }
  private acceptListener(clusterId: string, r: HAProxyListenerRevision): void {
    if (
      r?.organization_id !== this.organizationId ||
      r.cluster_id !== clusterId ||
      !validHTTPSListeners(r.listeners) ||
      !Number.isInteger(r.number) ||
      r.number < 1 ||
      r.number > 1000000 ||
      typeof r.rollout_id !== "string" ||
      ![
        "running",
        "rolling_back",
        "succeeded",
        "rolled_back",
        "rollback_failed",
      ].includes(r.status)
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid listener revision.",
      );
  }
  async deployListeners(
    clusterId: string,
    input: HAProxyListenerInput,
    rollbackId?: string,
  ): Promise<HAProxyListenerRevision> {
    const suffix = rollbackId
      ? `/versions/${encodeURIComponent(rollbackId)}/rollback`
      : "";
    const result = await this.send<{ revision: HAProxyListenerRevision }>(
      `/clusters/${encodeURIComponent(clusterId)}/listeners${suffix}`,
      { method: "POST", body: JSON.stringify(input) },
    );
    this.acceptListener(clusterId, result.revision);
    if (
      result.revision.request_id !== input.request_id ||
      result.revision.number !== input.expected_revision + 1 ||
      result.revision.rollback_of !== (rollbackId || undefined)
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid listener request identity.",
      );
    return result.revision;
  }
  async certificates(
    clusterId: string,
    signal?: AbortSignal,
  ): Promise<HAProxyACMEView> {
    const result = await this.send<HAProxyACMEView>(
      `/clusters/${encodeURIComponent(clusterId)}/certificates`,
      signal ? { signal } : {},
    );
    if (!validACMEView(result, this.organizationId, clusterId))
      throw new HAProxyError(
        502,
        "HAProxy returned invalid certificate automation.",
      );
    return result;
  }
  async setCertificates(
    clusterId: string,
    input: HAProxyACMEInput,
  ): Promise<HAProxyACMEPlan> {
    const result = await this.send<{ plan: HAProxyACMEPlan }>(
      `/clusters/${encodeURIComponent(clusterId)}/certificates`,
      { method: "POST", body: JSON.stringify(input) },
    );
    if (
      !validACMEView(
        { plan: result.plan, options: [] },
        this.organizationId,
        clusterId,
      ) ||
      result.plan.version !== input.expected_version + 1
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid certificate request identity.",
      );
    return result.plan;
  }
  async applications(
    clusterId: string,
    signal?: AbortSignal,
  ): Promise<HAProxyApplicationRevision[]> {
    const result = await this.send<{ revisions: HAProxyApplicationRevision[] }>(
      `/clusters/${encodeURIComponent(clusterId)}/applications`,
      signal ? { signal } : {},
    );
    if (!Array.isArray(result.revisions) || result.revisions.length > 256)
      throw new HAProxyError(
        502,
        "HAProxy returned invalid application history.",
      );
    for (const revision of result.revisions)
      this.acceptApplication(clusterId, revision);
    remember(this.applicationRecords, clusterId, result.revisions);
    return result.revisions;
  }
  async incidents(
    clusterId: string,
    before?: number,
    signal?: AbortSignal,
  ): Promise<HAProxyIncidentPage> {
    const suffix = before ? `?before=${encodeURIComponent(before)}` : "";
    const result = await this.send<unknown>(
      `/clusters/${encodeURIComponent(clusterId)}/incidents${suffix}`,
      signal ? { signal } : {},
    );
    if (!validIncidentPage(result, this.organizationId, clusterId))
      throw new HAProxyError(502, "HAProxy returned invalid incident history.");
    return result;
  }
  async applicationGeography(
    clusterId: string,
    applicationId: string,
    filters: HAProxyRequestFilters,
    signal?: AbortSignal,
  ): Promise<HAProxyGeographyTraffic> {
    const result = await this.send<unknown>(
      `/clusters/${encodeURIComponent(clusterId)}/applications/${encodeURIComponent(applicationId)}/geography?${requestQuery(filters)}`,
      signal ? { signal } : {},
    );
    if (
      !validGeographyTraffic(
        result,
        this.organizationId,
        clusterId,
        applicationId,
      )
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid geography analytics.",
      );
    remember(
      this.geographyAnalytics,
      `${clusterId}/${applicationId}?${requestQuery(filters)}`,
      result,
    );
    return result;
  }
  async applicationTrafficRules(
    cluster: string,
    app: string,
    revision: string,
    signal?: AbortSignal,
  ): Promise<TrafficRuleCounts> {
    const result = await this.send<unknown>(
      `/clusters/${encodeURIComponent(cluster)}/applications/${encodeURIComponent(app)}/versions/${encodeURIComponent(revision)}/traffic-rules`,
      signal ? { signal } : {},
    );
    if (!validTrafficCounts(result, this.organizationId, cluster, app))
      throw new HAProxyError(
        502,
        "HAProxy returned invalid traffic rule statistics.",
      );
    return result;
  }
  async requestTraffic(
    clusterId: string,
    applicationId: string,
    filters: HAProxyRequestFilters,
    signal?: AbortSignal,
  ): Promise<HAProxyRequestTraffic> {
    const result = await this.send<unknown>(
      `/clusters/${encodeURIComponent(clusterId)}/applications/${encodeURIComponent(applicationId)}/traffic?${requestQuery(filters)}`,
      signal ? { signal } : {},
    );
    if (
      !validRequestTraffic(
        result,
        this.organizationId,
        clusterId,
        applicationId,
      )
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid request analytics.",
      );
    remember(
      this.requestAnalytics,
      `${clusterId}/${applicationId}?${requestQuery(filters)}`,
      result,
    );
    return result;
  }
  async applicationRequests(
    clusterId: string,
    applicationId: string,
    filters: HAProxyRequestFilters,
    end?: number,
    before?: string,
    signal?: AbortSignal,
  ): Promise<HAProxyRequestPage> {
    const result = await this.send<unknown>(
      `/clusters/${encodeURIComponent(clusterId)}/applications/${encodeURIComponent(applicationId)}/requests?${requestQuery(filters, end, before)}`,
      signal ? { signal } : {},
    );
    if (
      !validRequestPage(result, this.organizationId, clusterId, applicationId)
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid request search results.",
      );
    remember(
      this.requestPages,
      `${clusterId}/${applicationId}?${requestQuery(filters, end, before)}`,
      result,
    );
    return result;
  }
  async applicationHealth(
    clusterId: string,
    signal?: AbortSignal,
  ): Promise<HAProxyApplicationHealth[]> {
    const result = await this.send<{
      organization_id: string;
      cluster_id: string;
      applications: HAProxyApplicationHealth[];
    }>(
      `/clusters/${encodeURIComponent(clusterId)}/applications/status`,
      signal ? { signal } : {},
    );
    if (
      result.organization_id !== this.organizationId ||
      result.cluster_id !== clusterId ||
      !validApplicationHealth(result.applications)
    )
      throw new HAProxyError(
        502,
        "HAProxy returned invalid application health.",
      );
    remember(this.applicationChecks, clusterId, result.applications);
    return result.applications;
  }
  async applicationDNSIntent(
    clusterId: string,
    applicationId: string,
    input:
      | {
          request_id: string;
          revision_id: string;
          zone_id: string;
          working_version_id: string;
          record_type: "A" | "AAAA" | "CNAME";
          value: string;
          ttl: number;
        }
      | {
          request_id: string;
          revision_id: string;
          zone_id: string;
          working_version_id: string;
          issuer: string;
        },
  ): Promise<string> {
    const result = await this.send<{
      intent: string;
      organization_id: string;
      cluster_id: string;
      application_id: string;
      request_id: string;
    }>(
      `/clusters/${encodeURIComponent(clusterId)}/applications/${encodeURIComponent(applicationId)}/${"issuer" in input ? "dns-delegation" : "dns-intent"}`,
      { method: "POST", body: JSON.stringify(input) },
    );
    if (
      result.organization_id !== this.organizationId ||
      result.cluster_id !== clusterId ||
      result.application_id !== applicationId ||
      result.request_id !== input.request_id ||
      typeof result.intent !== "string" ||
      result.intent.length > 8192 ||
      !/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{43}$/.test(result.intent)
    )
      throw new HAProxyError(502, "HAProxy returned an invalid DNS intent.");
    return result.intent;
  }
  private acceptApplication(
    clusterId: string,
    revision: HAProxyApplicationRevision,
  ): void {
    if (
      revision?.organization_id !== this.organizationId ||
      revision.cluster_id !== clusterId ||
      revision.spec?.id !== revision.application_id ||
      !validApplication(revision.spec) ||
      (revision.deleted !== undefined &&
        typeof revision.deleted !== "boolean") ||
      !Number.isInteger(revision.number) ||
      revision.number < 1 ||
      revision.number > 64 ||
      typeof revision.rollout_id !== "string" ||
      ![
        "running",
        "rolling_back",
        "succeeded",
        "rolled_back",
        "rollback_failed",
      ].includes(revision.status)
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid application revision.",
      );
  }
  async deployApplication(
    clusterId: string,
    applicationId: string,
    input: HAProxyApplicationInput,
    rollbackId?: string,
  ): Promise<HAProxyApplicationRevision> {
    const path = applicationId
      ? `/${encodeURIComponent(applicationId)}/${rollbackId ? `versions/${encodeURIComponent(rollbackId)}/rollback` : "deploy"}`
      : "";
    const result = await this.send<{ revision: HAProxyApplicationRevision }>(
      `/clusters/${encodeURIComponent(clusterId)}/applications${path}`,
      { method: "POST", body: JSON.stringify(input) },
    );
    this.acceptApplication(clusterId, result.revision);
    if (
      result.revision.request_id !== input.request_id ||
      (applicationId && result.revision.application_id !== applicationId) ||
      result.revision.number !== input.expected_revision + 1 ||
      result.revision.rollback_of !== (rollbackId || undefined)
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid application request identity.",
      );
    return result.revision;
  }
  async applicationLifecycle(
    clusterId: string,
    applicationId: string,
    input: HAProxyApplicationLifecycleInput,
  ): Promise<HAProxyApplicationRevision> {
    const result = await this.send<{ revision: HAProxyApplicationRevision }>(
      `/clusters/${encodeURIComponent(clusterId)}/applications/${encodeURIComponent(applicationId)}/lifecycle`,
      { method: "POST", body: JSON.stringify(input) },
    );
    this.acceptApplication(clusterId, result.revision);
    if (
      result.revision.request_id !== input.request_id ||
      result.revision.application_id !== applicationId ||
      result.revision.number !== input.expected_revision + 1 ||
      !!result.revision.deleted !== (input.action === "delete") ||
      (input.action !== "delete" &&
        !!result.revision.spec.disabled !== (input.action === "disable"))
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid application lifecycle identity.",
      );
    return result.revision;
  }
  private acceptRollout(clusterId: string, value: HAProxyRollout): void {
    const entrypoint = value?.entrypoint;
    if (
      value?.organization_id !== this.organizationId ||
      value.cluster_id !== clusterId ||
      !Array.isArray(value.nodes) ||
      value.nodes.length < 2 ||
      value.nodes.length > 12 ||
      !validConfigurationDocument(value.document) ||
      new Set(value.nodes.map((node) => node.node_id)).size !==
        value.nodes.length ||
      (entrypoint &&
        (entrypoint.cluster_id === clusterId ||
          entrypoint.topology?.control?.node_id !== entrypoint.node_id ||
          !validConfigurationDocument(entrypoint.baseline) ||
          value.nodes.some((node) => node.node_id === entrypoint.node_id))) ||
      value.nodes.some((node) =>
        [node.maintenance, node.rollback_maintenance].some(
          (maintenance) =>
            maintenance &&
            (!entrypoint ||
              maintenance.organization_id !== this.organizationId ||
              maintenance.cluster_id !== clusterId ||
              maintenance.node_id !== node.node_id ||
              maintenance.controller_node_id !== entrypoint.node_id ||
              maintenance.controller_cluster_id !== entrypoint.cluster_id ||
              maintenance.binding_id !== entrypoint.binding_id),
        ),
      ) ||
      value.nodes.some(
        (node) =>
          [node.application_checks, node.rollback_application_checks].some(
            (checks) =>
              checks !== undefined &&
              (!validApplicationProbeGrants(checks) ||
                checks.some(
                  (c) =>
                    !acmeUUID(c.application_id) ||
                    typeof c.spec_digest !== "string" ||
                    !/^[a-f0-9]{64}$/.test(c.spec_digest),
                )),
          ) ||
          [
            node.application_verification,
            node.rollback_application_verification,
          ].some(
            (gate) => gate !== undefined && !validApplicationVerification(gate),
          ),
      )
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid cluster rollout.",
      );
  }
  async rollouts(
    clusterId: string,
    signal?: AbortSignal,
  ): Promise<HAProxyRollout[]> {
    const result = await this.send<{ rollouts: HAProxyRollout[] }>(
      `/clusters/${encodeURIComponent(clusterId)}/rollouts`,
      signal ? { signal } : {},
    );
    if (!Array.isArray(result.rollouts) || result.rollouts.length > 20)
      throw new HAProxyError(502, "HAProxy returned invalid rollout history.");
    result.rollouts.forEach((value) => this.acceptRollout(clusterId, value));
    remember(this.clusterRollouts, clusterId, result.rollouts);
    return result.rollouts;
  }
  async startRollout(
    clusterId: string,
    input: HAProxyRolloutInput,
  ): Promise<HAProxyRollout> {
    const result = await this.send<{ rollout: HAProxyRollout }>(
      `/clusters/${encodeURIComponent(clusterId)}/rollouts`,
      { method: "POST", body: JSON.stringify(input) },
    );
    this.acceptRollout(clusterId, result.rollout);
    if (
      result.rollout.request_id !== input.request_id ||
      !!result.rollout.entrypoint !== !!input.entrypoint ||
      (input.entrypoint &&
        (result.rollout.entrypoint!.node_id !== input.entrypoint.node_id ||
          result.rollout.entrypoint!.binding_id !==
            input.entrypoint.binding_id ||
          result.rollout.entrypoint!.expected_generation !==
            input.entrypoint.expected_generation ||
          result.rollout.entrypoint!.expected_artifact_id !==
            input.entrypoint.expected_artifact_id ||
          result.rollout.entrypoint!.expected_version !==
            input.entrypoint.expected_version ||
          result.rollout.drain_timeout_seconds !==
            (input.drain_timeout_seconds || 30))) ||
      result.rollout.nodes.length !== input.nodes.length ||
      result.rollout.nodes.some(
        (node) =>
          !input.nodes.some((target) => target.node_id === node.node_id),
      )
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid rollout request identity.",
      );
    remember(
      this.clusterRollouts,
      clusterId,
      [
        result.rollout,
        ...(this.cachedRollouts(clusterId) ?? []).filter(
          (value) => value.id !== result.rollout.id,
        ),
      ].slice(0, 20),
    );
    return result.rollout;
  }
  cachedConfiguration(nodeId: string): HAProxyConfigurationView | undefined {
    return this.configurations.get(nodeId);
  }
  async configuration(
    nodeId: string,
    signal?: AbortSignal,
  ): Promise<HAProxyConfigurationView> {
    const result = await this.send<HAProxyConfigurationView>(
      `/nodes/${encodeURIComponent(nodeId)}/configuration`,
      signal ? { signal } : {},
    );
    if (
      result.control?.node_id !== nodeId ||
      !Array.isArray(result.versions) ||
      result.versions.length > 50 ||
      result.versions.some(
        (revision) =>
          revision.node_id !== nodeId ||
          revision.organization_id !== this.organizationId ||
          !validConfigurationDocument(revision.document),
      ) ||
      typeof result.connected !== "boolean" ||
      typeof result.fresh !== "boolean" ||
      typeof result.configuration_enabled !== "boolean" ||
      (result.control.discovery?.configuration &&
        !validTLSInventory(result.control.discovery.configuration)) ||
      (result.control.discovery?.configuration?.document &&
        !validConfigurationDocument(
          result.control.discovery.configuration.document,
        ))
    ) {
      throw new HAProxyError(
        502,
        "HAProxy returned invalid configuration history.",
      );
    }
    remember(this.configurations, nodeId, result);
    return result;
  }
  async saveConfiguration(
    nodeId: string,
    input: HAProxyConfigurationInput,
    rollbackId?: string,
  ): Promise<HAProxyOperation> {
    const path = `/nodes/${encodeURIComponent(nodeId)}/configuration${rollbackId ? `/versions/${encodeURIComponent(rollbackId)}/rollback` : ""}`;
    const result = await this.send<{ operation: HAProxyOperation }>(path, {
      method: "POST",
      body: JSON.stringify(input),
    });
    if (
      result.operation?.node_id !== nodeId ||
      result.operation.organization_id !== this.organizationId ||
      result.operation.request_id !== input.request_id ||
      result.operation.kind !== "configuration"
    )
      throw new HAProxyError(
        502,
        "HAProxy returned an invalid configuration operation.",
      );
    return result.operation;
  }

  async operate(
    nodeId: string,
    input: HAProxyOperationInput,
  ): Promise<HAProxyOperation> {
    const result = await this.send<{ operation: HAProxyOperation }>(
      `/nodes/${encodeURIComponent(nodeId)}/operations`,
      { method: "POST", body: JSON.stringify(input) },
    );
    if (
      result.operation?.node_id !== nodeId ||
      result.operation.organization_id !== this.organizationId ||
      result.operation.request_id !== input.request_id
    )
      throw new HAProxyError(502, "HAProxy returned an invalid operation.");
    return result.operation;
  }

  private recordSnapshot(snapshot: HAProxySnapshot): void {
    const pair = this.cachedObservations(snapshot.node_id);
    if (
      pair.current?.observed_at &&
      (!snapshot.observed_at ||
        Date.parse(snapshot.observed_at) < Date.parse(pair.current.observed_at))
    )
      return;
    remember(this.observations, snapshot.node_id, {
      current: snapshot,
      previous:
        snapshot.observed_at === pair.current?.observed_at
          ? pair.previous
          : pair.current,
    });
    if (!this.clusteredNodes.has(snapshot.node_id)) {
      this.recordLiveNodes([
        snapshotLive(
          snapshot,
          this.cachedObservations(snapshot.node_id).previous,
        ),
      ]);
    }
  }

  async snapshot(
    nodeId: string,
    signal?: AbortSignal,
  ): Promise<HAProxySnapshot> {
    const result = await this.send<HAProxySnapshot>(
      `/nodes/${encodeURIComponent(nodeId)}/metrics`,
      signal ? { signal } : {},
    );
    if (result.node_id !== nodeId || !Array.isArray(result.data?.result))
      throw new HAProxyError(502, "HAProxy returned an invalid observation.");
    this.recordSnapshot(result);
    return result;
  }

  cachedHistory(nodeId: string, range: HAProxyHistoryRange) {
    return this.histories.get(`${nodeId}/${range}`);
  }

  async history(
    nodeId: string,
    range: HAProxyHistoryRange,
    signal?: AbortSignal,
  ): Promise<HAProxyHistory> {
    const result = await this.send<HAProxyHistory>(
      `/nodes/${encodeURIComponent(nodeId)}/history?range=${range}`,
      signal ? { signal } : {},
    );
    if (
      result.node_id !== nodeId ||
      result.range !== range ||
      !Array.isArray(result.series)
    )
      throw new HAProxyError(502, "HAProxy returned invalid history.");
    const merged = mergeLiveHistory(result, this.liveFrames.get(nodeId) ?? []);
    remember(this.histories, `${nodeId}/${range}`, {
      data: merged,
      loadedAt: Date.now(),
    });
    return merged;
  }

  agentSetup(signal?: AbortSignal): Promise<HAProxyAgentSetup> {
    return this.send("/agent-setup", signal ? { signal } : {});
  }

  enrollmentStatus(
    token: string,
    signal?: AbortSignal,
  ): Promise<HAProxyEnrollmentStatus> {
    return this.send("/enrollment-status", {
      method: "POST",
      body: JSON.stringify({ token }),
      ...(signal ? { signal } : {}),
    });
  }

  createCluster(name: string): Promise<{ cluster: HAProxyCluster }> {
    return this.send("/clusters", {
      method: "POST",
      body: JSON.stringify({ name }),
    });
  }

  enroll(clusterId: string): Promise<{ token: string; expires_at: string }> {
    return this.send(
      `/clusters/${encodeURIComponent(clusterId)}/enrollment-tokens`,
      {
        method: "POST",
        body: JSON.stringify({ ttl_seconds: 600 }),
      },
    );
  }

  async revoke(nodeId: string): Promise<void> {
    await this.send(`/nodes/${encodeURIComponent(nodeId)}/revoke`, {
      method: "POST",
      body: "{}",
    });
    this.observations.delete(nodeId);
    this.discoveries.delete(nodeId);
    this.configurations.delete(nodeId);
    this.liveFrames.delete(nodeId);
    for (const key of this.histories.keys())
      if (key.startsWith(`${nodeId}/`)) this.histories.delete(key);
  }

  async stream(
    nodeId: string,
    signal: AbortSignal,
    onSnapshot: (snapshot: HAProxySnapshot) => void,
    onUnavailable: () => void,
  ): Promise<void> {
    return this.streamEvents(
      `/nodes/${encodeURIComponent(nodeId)}/events`,
      signal,
      (data) => {
        const snapshot = data as HAProxySnapshot;
        if (
          snapshot.node_id !== nodeId ||
          !Array.isArray(snapshot.data?.result)
        )
          throw new Error("Invalid observation");
        this.recordSnapshot(snapshot);
        onSnapshot(snapshot);
      },
      onUnavailable,
    );
  }

  clusterStream(
    clusterId: string,
    nodeIds: string[],
    signal: AbortSignal,
    onFrame: (frame: HAProxyClusterLive) => void,
    onUnavailable: () => void,
  ): Promise<void> {
    const allowed = new Set(nodeIds);
    if (
      !nodeIds.length ||
      nodeIds.length > 12 ||
      allowed.size !== nodeIds.length
    )
      return Promise.reject(new Error("Select up to 12 distinct nodes"));
    for (const id of nodeIds)
      this.clusteredNodes.set(id, (this.clusteredNodes.get(id) ?? 0) + 1);
    return this.streamEvents(
      `/clusters/${encodeURIComponent(clusterId)}/events?nodes=${encodeURIComponent(nodeIds.join(","))}`,
      signal,
      (data) => {
        const frame = data as HAProxyClusterLive;
        const seen = new Set<string>();
        if (
          frame.cluster_id !== clusterId ||
          !Array.isArray(frame.nodes) ||
          frame.nodes.length !== allowed.size
        )
          throw new Error("Invalid cluster observation");
        // Validate the whole frame before caching any node's measurements.
        for (const node of frame.nodes) {
          if (
            !allowed.has(node.node_id) ||
            seen.has(node.node_id) ||
            !Array.isArray(node.series) ||
            node.series.length > 62 ||
            (node.observed_at !== null &&
              !Number.isFinite(Date.parse(node.observed_at))) ||
            typeof node.fresh !== "boolean" ||
            typeof node.scrape_ok !== "boolean" ||
            typeof node.revoked !== "boolean"
          )
            throw new Error("Invalid cluster node");
          seen.add(node.node_id);
          for (const point of node.series)
            if (
              !point.metric ||
              typeof point.metric.series !== "string" ||
              !Number.isFinite(point.value) ||
              point.value < 0
            )
              throw new Error("Invalid live value");
        }
        this.recordLiveNodes(frame.nodes);
        onFrame(frame);
      },
      onUnavailable,
    ).finally(() => {
      for (const id of nodeIds) {
        const count = (this.clusteredNodes.get(id) ?? 1) - 1;
        if (count > 0) this.clusteredNodes.set(id, count);
        else this.clusteredNodes.delete(id);
      }
    });
  }

  private async streamEvents(
    path: string,
    signal: AbortSignal,
    onData: (data: unknown) => void,
    onUnavailable: () => void,
  ): Promise<void> {
    const combined = AbortSignal.any([signal, this.controller.signal]);
    while (!combined.aborted) {
      if (globalThis.document?.visibilityState === "hidden") {
        await new Promise<void>((resolve) => {
          const finish = () => {
            if (
              globalThis.document?.visibilityState === "hidden" &&
              !combined.aborted
            )
              return;
            globalThis.document?.removeEventListener(
              "visibilitychange",
              finish,
            );
            combined.removeEventListener("abort", finish);
            resolve();
          };
          globalThis.document?.addEventListener("visibilitychange", finish);
          combined.addEventListener("abort", finish, { once: true });
          finish();
        });
        if (combined.aborted) return;
      }
      const connection = new AbortController();
      const pause = () => {
        if (globalThis.document?.visibilityState === "hidden")
          connection.abort();
      };
      globalThis.document?.addEventListener("visibilitychange", pause);
      try {
        const response = await fetch(`${base}${path}`, {
          credentials: "same-origin",
          redirect: "error",
          headers: {
            Accept: "text/event-stream",
            "X-Organization-ID": this.organizationId,
            "X-HAProxy-Session": "local",
          },
          signal: AbortSignal.any([combined, connection.signal]),
        });
        if (!response.ok || !response.body)
          throw new HAProxyError(
            response.status,
            "Live observations are unavailable.",
          );
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let pending = "";
        try {
          while (!combined.aborted && !connection.signal.aborted) {
            const part = await reader.read();
            if (part.done) break;
            pending = (
              pending + decoder.decode(part.value, { stream: true })
            ).replace(/\r\n/g, "\n");
            if (pending.length > 2 * 1024 * 1024)
              throw new Error("Observation exceeds limit");
            let boundary: number;
            while ((boundary = pending.indexOf("\n\n")) !== -1) {
              const event = pending.slice(0, boundary);
              pending = pending.slice(boundary + 2);
              const kind = event
                .split("\n")
                .find((line) => line.startsWith("event:"))
                ?.slice(6)
                .trim();
              if (kind === "unavailable") onUnavailable();
              if (
                kind === "metrics" &&
                !combined.aborted &&
                !connection.signal.aborted
              )
                onData(
                  JSON.parse(
                    event
                      .split("\n")
                      .filter((line) => line.startsWith("data:"))
                      .map((line) => line.slice(5).trimStart())
                      .join("\n"),
                  ),
                );
            }
          }
        } finally {
          await reader.cancel().catch(() => {});
          reader.releaseLock();
        }
      } catch {
        if (combined.aborted) return;
      } finally {
        globalThis.document?.removeEventListener("visibilitychange", pause);
      }
      if (combined.aborted) return;
      if (globalThis.document?.visibilityState === "hidden") continue;
      onUnavailable();
      if (combined.aborted) return;
      await new Promise<void>((resolve) => {
        const finish = () => {
          clearTimeout(timer);
          combined.removeEventListener("abort", finish);
          resolve();
        };
        const timer = setTimeout(finish, 1000);
        combined.addEventListener("abort", finish, { once: true });
      });
    }
  }

  private acceptSession(session: BrowserSession): void {
    this.controller.signal.throwIfAborted();
    if (
      session.organization_id !== this.organizationId ||
      typeof session.csrf_token !== "string" ||
      !session.csrf_token ||
      !session.capabilities?.includes("haproxy.read")
    ) {
      throw new HAProxyError(
        403,
        "HAProxy access could not be verified for this organization.",
      );
    }
    this.session = session;
    this.managing = session.capabilities.includes("haproxy.manage");
    logoutContext = {
      csrf: session.csrf_token,
      organization: this.organizationId,
    };
  }

  private async send<T>(
    path: string,
    init: RequestInit = {},
    retryHandoff = true,
  ): Promise<T> {
    this.controller.signal.throwIfAborted();
    init.signal?.throwIfAborted();
    const browserHandoff = path.startsWith("/local/");
    const authentication = this.authentication;
    if (!browserHandoff) {
      // Authentication can replace the browser cookie and CSRF together. Keep background
      // reads and runtime writes behind it, even when MFA cancels an elevation.
      await authentication.catch(() => {});
      this.controller.signal.throwIfAborted();
      init.signal?.throwIfAborted();
    }
    const sessionAtRequest = this.session;
    const headers = new Headers(init.headers);
    headers.set("Accept", "application/json");
    headers.set("X-HAProxy-Session", "local");
    headers.set("X-Organization-ID", this.organizationId);
    if (init.body) headers.set("Content-Type", "application/json");
    if (init.method && !["GET", "HEAD"].includes(init.method) && this.session)
      headers.set("X-CSRF-Token", this.session.csrf_token);
    let response: Response;
    try {
      response = await fetch(`${base}${path}`, {
        ...init,
        credentials: "same-origin",
        redirect: "error",
        headers,
        signal: AbortSignal.any([
          this.controller.signal,
          ...(init.signal ? [init.signal] : []),
          AbortSignal.timeout(10_000),
        ]),
      });
    } catch (error) {
      if (this.controller.signal.aborted) throw error;
      throw new HAProxyError(
        503,
        "HAProxy could not connect. Check your connection and retry.",
      );
    }
    this.controller.signal.throwIfAborted();
    if (!response.ok) {
      if (response.status === 401)
        globalThis.window?.dispatchEvent(new Event("haproxy-session-expired"));
      if (
        !browserHandoff &&
        retryHandoff &&
        (!init.method || init.method === "GET") &&
        (response.status === 401 || response.status === 403) &&
        (this.authentication !== authentication ||
          this.session !== sessionAtRequest)
      ) {
        // An in-flight read may still carry the previous cookie. Retry it once
        // after the replacement; mutations are never replayed here.
        return this.send<T>(path, init, false);
      }
      const message =
        response.status === 401
          ? "HAProxy access expired. Reconnect to continue."
          : response.status === 403
            ? "Your role cannot perform this HAProxy action."
            : response.status === 429
              ? "HAProxy is busy. Retry shortly."
              : response.status === 409
                ? "HAProxy configuration or runtime state changed. Refresh discovery and try the change again."
                : "HAProxy is temporarily unavailable. Retry the connection.";
      throw new HAProxyError(response.status, message);
    }
    if (response.status === 204) return undefined as T;
    try {
      const payload = (await response.json()) as T;
      this.controller.signal.throwIfAborted();
      init.signal?.throwIfAborted();
      return payload;
    } catch (error) {
      if (this.controller.signal.aborted) throw error;
      throw new HAProxyError(
        502,
        "HAProxy returned an unreadable response. Retry the connection.",
      );
    }
  }
}

export function validConfigurationDocument(
  value: unknown,
): value is HAProxyConfigurationDocument {
  if (
    !value ||
    typeof value !== "object" ||
    !("backends" in value) ||
    !Array.isArray(value.backends) ||
    value.backends.length > 32 ||
    ("listeners" in value && !validHTTPSListeners(value.listeners)) ||
    ("applications" in value &&
      (!Array.isArray(value.applications) ||
        value.applications.length > 16 ||
        !value.applications.every(validApplication)))
  )
    return false;
  let total = 0;
  return value.backends.every(
    (backend) =>
      backend &&
      typeof backend.name === "string" &&
      ["roundrobin", "leastconn", "first"].includes(backend.algorithm) &&
      Array.isArray(backend.servers) &&
      backend.servers.every(
        (server: HAProxyConfigurationServer) =>
          ++total <= 128 &&
          server &&
          typeof server.name === "string" &&
          typeof server.address === "string" &&
          Number.isInteger(server.port) &&
          server.port >= 0 &&
          server.port <= 65535 &&
          (server.forward_incoming_port === undefined ||
            typeof server.forward_incoming_port === "boolean") &&
          (server.port === 0) === (server.forward_incoming_port === true) &&
          Number.isInteger(server.weight) &&
          server.weight >= 0 &&
          server.weight <= 256 &&
          typeof server.maintenance === "boolean",
      ),
  );
}

export function validApplication(value: unknown): value is HAProxyApplication {
  if (!value || typeof value !== "object") return false;
  const a = value as HAProxyApplication;
  return (
    validTrafficPolicy(a.traffic_policy, a.path_prefix) &&
    (a.traffic_policy === undefined || a.request_telemetry === true) &&
    typeof a.id === "string" &&
    (a.disabled === undefined || typeof a.disabled === "boolean") &&
    (a.request_telemetry === undefined ||
      typeof a.request_telemetry === "boolean") &&
    (a.verification === undefined ||
      (a.verification !== null &&
        typeof a.verification === "object" &&
        typeof a.verification.http === "boolean" &&
        typeof a.verification.https === "boolean" &&
        (a.verification.entrypoint === undefined ||
          typeof a.verification.entrypoint === "boolean") &&
        (a.verification.http || a.verification.https))) &&
    typeof a.name === "string" &&
    a.name.length > 0 &&
    a.name.length <= 80 &&
    typeof a.listener === "string" &&
    typeof a.hostname === "string" &&
    /^[a-z0-9.-]{1,253}$/.test(a.hostname) &&
    typeof a.path_prefix === "string" &&
    /^\/[A-Za-z0-9/._~-]*$/.test(a.path_prefix) &&
    typeof a.health_path === "string" &&
    /^\/[A-Za-z0-9/._~-]*$/.test(a.health_path) &&
    ["roundrobin", "leastconn", "first"].includes(a.algorithm) &&
    Array.isArray(a.servers) &&
    a.servers.length > 0 &&
    a.servers.length <= 16 &&
    a.servers.every((s) => !s.forward_incoming_port) &&
    validConfigurationDocument({
      backends: [
        { name: "application", algorithm: a.algorithm, servers: a.servers },
      ],
    })
  );
}

export function validHTTPSListeners(
  value: unknown,
): value is HAProxyHTTPSListener[] {
  if (!Array.isArray(value) || value.length > 4) return false;
  return (
    value.every(
      (l: HAProxyHTTPSListener) =>
        l &&
        typeof l.name === "string" &&
        /^[-A-Za-z0-9_.:]{1,128}$/.test(l.name) &&
        typeof l.address === "string" &&
        /^(\*|[0-9a-fA-F.:]+)$/.test(l.address) &&
        Number.isInteger(l.port) &&
        l.port > 0 &&
        l.port <= 65535 &&
        typeof l.certificate_ref === "string" &&
        /^[-A-Za-z0-9_.:]{1,64}$/.test(l.certificate_ref) &&
        typeof l.certificate_sha256 === "string" &&
        (l.node_certificates && Object.keys(l.node_certificates).length > 0
          ? l.certificate_sha256 === "" &&
            Object.entries(l.node_certificates).length <= 12 &&
            Object.entries(l.node_certificates).every(
              ([id, hash]) =>
                acmeUUID(id) &&
                typeof hash === "string" &&
                /^[a-f0-9]{64}$/.test(hash),
            )
          : /^[a-f0-9]{64}$/.test(l.certificate_sha256)),
    ) &&
    new Set(value.map((l) => l.name)).size === value.length &&
    new Set(value.map((l) => l.port)).size === value.length
  );
}
function validTLSInventory(value: HAProxyConfigurationState): boolean {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  return (
    (value.traffic_rules === undefined ||
      validTrafficCapabilities(value.traffic_rules)) &&
    (value.node_id === undefined || acmeUUID(value.node_id)) &&
    (value.acme_grants === undefined ||
      (Array.isArray(value.acme_grants) &&
        value.acme_grants.length <= 4 &&
        value.acme_grants.every(
          (g) =>
            acmeName(g.issuer) &&
            acmeName(g.listener) &&
            ["staging", "production"].includes(g.environment) &&
            /^[a-f0-9]{64}$/.test(g.policy_digest) &&
            validACMEChallenges(g.challenges),
        ))) &&
    (value.listener_control_version === undefined ||
      value.listener_control_version === 1) &&
    (value.listener_bindings === undefined ||
      (Array.isArray(value.listener_bindings) &&
        value.listener_bindings.length <= 4 &&
        value.listener_bindings.every((b) =>
          validHTTPSListeners([
            {
              ...b,
              certificate_ref: "test",
              certificate_sha256: "a".repeat(64),
            },
          ]),
        ))) &&
    (value.tls_certificates === undefined ||
      (Array.isArray(value.tls_certificates) &&
        value.tls_certificates.length <= 16 &&
        value.tls_certificates.every(
          (c) =>
            c &&
            typeof c.reference === "string" &&
            /^[-A-Za-z0-9_.:]{1,64}$/.test(c.reference) &&
            typeof c.sha256 === "string" &&
            (c.sha256 === "" || /^[a-f0-9]{64}$/.test(c.sha256)) &&
            Array.isArray(c.dns_names) &&
            c.dns_names.length <= 32 &&
            c.dns_names.every(
              (n) => typeof n === "string" && n.length > 0 && n.length <= 253,
            ) &&
            Number.isSafeInteger(c.not_before_ms) &&
            Number.isSafeInteger(c.not_after_ms) &&
            [
              "",
              "certificate_unavailable",
              "certificate_invalid",
              "certificate_expired",
              "certificate_not_yet_valid",
            ].includes(c.error_code),
        ))) &&
    (value.tls_listeners === undefined ||
      (Array.isArray(value.tls_listeners) &&
        value.tls_listeners.length <= 4 &&
        value.tls_listeners.every(
          (l) =>
            l &&
            typeof l.name === "string" &&
            typeof l.loaded === "boolean" &&
            typeof l.sha256 === "string" &&
            (l.sha256 === "" || /^[a-f0-9]{64}$/.test(l.sha256)),
        )))
  );
}

const acmeUUID = (v: unknown): v is string =>
  typeof v === "string" &&
  /^[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}$/.test(v);
const acmeName = (v: unknown): v is string =>
  typeof v === "string" && /^[-A-Za-z0-9_.:]{1,128}$/.test(v);
const acmeHostname = (v: unknown): v is string =>
  typeof v === "string" &&
  v.length <= 253 &&
  v.split(".").every((l) => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(l));
function validACMEChallenges(v: HAProxyACMEChallenge[]): boolean {
  return (
    Array.isArray(v) &&
    v.length > 0 &&
    v.length <= 8 &&
    new Set(v.map((c) => c.hostname)).size === v.length &&
    v.every((c) => c && acmeHostname(c.hostname) && acmeHostname(c.target))
  );
}
export function validACMEView(
  v: HAProxyACMEView,
  org: string,
  cluster: string,
): boolean {
  if (
    !v ||
    !Array.isArray(v.options) ||
    v.options.length > 16 ||
    v.options.some(
      (o) =>
        !o ||
        !acmeName(o.issuer) ||
        !["staging", "production"].includes(o.environment) ||
        !validACMEChallenges(o.challenges) ||
        !validHTTPSListeners([
          {
            ...o.binding,
            certificate_ref: "check",
            certificate_sha256: "a".repeat(64),
          },
        ]),
    )
  )
    return false;
  const p = v.plan;
  if (p === null) return true;
  if (
    !p ||
    !acmeUUID(p.id) ||
    p.organization_id !== org ||
    p.cluster_id !== cluster ||
    !Number.isSafeInteger(p.version) ||
    p.version < 1 ||
    typeof p.enabled !== "boolean" ||
    !Number.isSafeInteger(p.generation) ||
    p.generation < 1 ||
    !acmeName(p.issuer) ||
    !["production", "staging"].includes(p.environment) ||
    !["issuing", "ready", "deploying", "active", "error", "disabled"].includes(
      p.status,
    ) ||
    typeof p.error_code !== "string" ||
    p.error_code.length > 64 ||
    !validACMEChallenges(p.challenges) ||
    !Array.isArray(p.hostnames) ||
    p.hostnames.length < 1 ||
    p.hostnames.length > 8 ||
    !p.hostnames.every(acmeHostname) ||
    !validHTTPSListeners([
      {
        ...p.binding,
        certificate_ref: "check",
        certificate_sha256: "a".repeat(64),
      },
    ]) ||
    !Number.isSafeInteger(p.renew_at_ms) ||
    !Number.isSafeInteger(p.next_run_ms) ||
    !Array.isArray(p.jobs) ||
    p.jobs.length < 2 ||
    p.jobs.length > 12 ||
    new Set(p.jobs.map((j) => j.node_id)).size !== p.jobs.length ||
    !Array.isArray(p.history) ||
    p.history.length > 20
  )
    return false;
  const cert = (c: HAProxyTLSCertificate) =>
    c &&
    validTLSInventory({
      version: 1,
      artifact_id: "",
      observed_at_ms: 0,
      control_allowed: false,
      error_code: "",
      tls_certificates: [c],
    });
  return (
    p.jobs.every(
      (j) =>
        j &&
        acmeUUID(j.id) &&
        j.plan_id === p.id &&
        j.generation === p.generation &&
        acmeUUID(j.node_id) &&
        j.issuer === p.issuer &&
        j.listener === p.binding.name &&
        Array.isArray(j.hostnames) &&
        JSON.stringify(j.hostnames) === JSON.stringify(p.hostnames) &&
        /^[a-f0-9]{64}$/.test(j.policy_digest) &&
        /^acme-[a-f0-9]{40}$/.test(j.reference) &&
        ["pending", "issuing", "ready", "failed"].includes(j.status) &&
        typeof j.error_code === "string" &&
        j.error_code.length <= 64 &&
        Number.isSafeInteger(j.attempts) &&
        Number.isSafeInteger(j.next_try_ms) &&
        Number.isSafeInteger(j.deadline_ms) &&
        (!j.certificate ||
          (cert(j.certificate) && j.certificate.reference === j.reference)),
    ) &&
    p.history.every(
      (h) =>
        h &&
        Number.isSafeInteger(h.generation) &&
        h.generation <= p.generation &&
        acmeUUID(h.rollout_id) &&
        ["succeeded", "rolled_back", "rollback_failed"].includes(h.status) &&
        Number.isSafeInteger(h.completed_at_ms) &&
        h.certificates &&
        Object.keys(h.certificates).length <= 12 &&
        Object.entries(h.certificates).every(
          ([node, c]) => acmeUUID(node) && cert(c),
        ),
    )
  );
}
