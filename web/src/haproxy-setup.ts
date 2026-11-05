export const setupChecks = {
  metrics: {
    label: "HAProxy metrics",
    codes: {
      metrics_ready: ["ready", "The agent can read supported HAProxy metrics."],
      metrics_unavailable: [
        "error",
        "Check the metrics listener and reachability from the agent’s network.",
      ],
      metrics_pending: [
        "pending",
        "Waiting for the first local HAProxy scrape.",
      ],
    },
  },
  discovery: {
    label: "Configuration discovery",
    codes: {
      runtime_ready: ["ready", "The local Runtime API is readable."],
      runtime_not_configured: [
        "disabled",
        "Add this instance’s local Runtime API socket to the agent settings.",
      ],
      runtime_unavailable: [
        "error",
        "Check the Runtime API socket, its permissions and configured files.",
      ],
      unsupported_runtime: [
        "error",
        "This HAProxy version or runtime response is outside the supported profile.",
      ],
    },
  },
  files: {
    label: "Configuration files",
    codes: {
      files_ready: ["ready", "The explicitly configured files are readable."],
      files_not_configured: [
        "disabled",
        "List the HAProxy configuration files before enabling management.",
      ],
      files_unreadable: [
        "error",
        "Grant the agent read access to regular configuration files and their parent directories.",
      ],
    },
  },
  ownership: {
    label: "Configuration ownership",
    codes: {
      owner_manager: [
        "ready",
        "A host administrator declared this manager as the configuration writer.",
      ],
      owner_external: [
        "blocked",
        "Another tool owns configuration. Monitoring and separately granted runtime operations remain available.",
      ],
      owner_unconfirmed: [
        "warning",
        "Identify any GitOps, Ansible or ingress controller writer before enabling configuration edits.",
      ],
      automation_detected: [
        "warning",
        "Automation comments were found in a configuration header. Resolve the existing writer before declaring manager ownership.",
      ],
    },
  },
  runtime_control: {
    label: "Drain, restore and weights",
    codes: {
      runtime_control_ready: [
        "ready",
        "Local runtime control is enabled and the socket provides admin access.",
      ],
      runtime_control_disabled: [
        "disabled",
        "Enable runtime control locally when you want to manage backend servers.",
      ],
      runtime_control_unavailable: [
        "blocked",
        "Resolve configuration discovery and file access first.",
      ],
      runtime_admin_required: [
        "blocked",
        "The Runtime API socket must grant the agent admin access for these operations.",
      ],
      runtime_version_unsupported: [
        "blocked",
        "Runtime management currently requires the qualified HAProxy 3.4 profile.",
      ],
    },
  },
  configuration_control: {
    label: "Persistent configuration",
    codes: {
      configuration_control_ready: [
        "ready",
        "Local configuration control is enabled and Data Plane API discovery is compatible.",
      ],
      configuration_read_only: [
        "disabled",
        "The Data Plane API is readable. Persistent changes are not enabled locally.",
      ],
      configuration_control_disabled: [
        "disabled",
        "Enable configuration control locally after connecting the Data Plane API and resolving ownership.",
      ],
      configuration_unavailable: [
        "blocked",
        "Check the local Data Plane API socket, private credentials, policies and supported HAProxy profile.",
      ],
      configuration_unsupported: [
        "blocked",
        "The existing configuration is outside the editing profile. Keep monitoring it without changing its configuration.",
      ],
      configuration_owner_required: [
        "blocked",
        "Resolve file access and the external writer, then explicitly declare manager ownership on the host.",
      ],
    },
  },
} as const;

export type SetupCheckName = keyof typeof setupChecks;
export type SetupStatus =
  "ready" | "disabled" | "blocked" | "error" | "warning" | "pending";
export interface HAProxyNodeSetup {
  observed_at_ms: number;
  owner: "manager" | "external" | "unconfirmed";
  checks: Array<{ name: SetupCheckName; status: SetupStatus; code: string }>;
}

export function validNodeSetup(value: unknown): value is HAProxyNodeSetup {
  if (!value || typeof value !== "object") return false;
  const report = value as HAProxyNodeSetup;
  if (
    !Number.isSafeInteger(report.observed_at_ms) ||
    report.observed_at_ms <= 0 ||
    !["manager", "external", "unconfirmed"].includes(report.owner) ||
    !Array.isArray(report.checks) ||
    report.checks.length !== 6
  )
    return false;
  const seen = new Set<string>();
  return report.checks.every((check) => {
    if (
      !check ||
      typeof check.name !== "string" ||
      !Object.hasOwn(setupChecks, check.name) ||
      seen.has(check.name)
    )
      return false;
    seen.add(check.name);
    const codes = setupChecks[check.name].codes as Record<
      string,
      readonly [string, string]
    >;
    return (
      typeof check.code === "string" &&
      Object.hasOwn(codes, check.code) &&
      codes[check.code]?.[0] === check.status
    );
  });
}

export function setupMessage(
  check: HAProxyNodeSetup["checks"][number],
): string {
  const codes = setupChecks[check.name].codes as Record<
    string,
    readonly [string, string]
  >;
  return codes[check.code]?.[1] ?? "Refresh the agent setup report.";
}
