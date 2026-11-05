import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { HAProxyNodeSetupPanel } from "./haproxy-node-setup";
import { validNodeSetup, type HAProxyNodeSetup } from "./haproxy-setup";

const now = Date.now();
const report = (): HAProxyNodeSetup => ({
  observed_at_ms: now,
  owner: "external",
  checks: [
    { name: "metrics", status: "ready", code: "metrics_ready" },
    { name: "discovery", status: "disabled", code: "runtime_not_configured" },
    { name: "files", status: "disabled", code: "files_not_configured" },
    { name: "ownership", status: "blocked", code: "owner_external" },
    {
      name: "runtime_control",
      status: "disabled",
      code: "runtime_control_disabled",
    },
    {
      name: "configuration_control",
      status: "disabled",
      code: "configuration_control_disabled",
    },
  ],
});

describe("existing HAProxy setup", () => {
  it("distinguishes working monitoring from intentionally disabled management", () => {
    const setup = report();
    expect(validNodeSetup(setup)).toBe(true);
    const html = renderToStaticMarkup(
      <HAProxyNodeSetupPanel setup={setup} connected now={now} />,
    );
    expect(html).toContain("Ready");
    expect(html).toContain("Not enabled");
    expect(html).toContain("Another tool owns configuration");
    expect(html).toContain("Setup guide");
    expect(html).not.toContain("Approve");
  });
  it("does not show stale or disconnected checks as ready", () => {
    for (const connected of [false, true]) {
      const html = renderToStaticMarkup(
        <HAProxyNodeSetupPanel
          setup={report()}
          connected={connected}
          now={now + 7000}
        />,
      );
      expect(html).toContain("Last reported");
      expect(html).not.toContain(">Ready<");
    }
    expect(renderToStaticMarkup(<HAProxyNodeSetupPanel connected />)).toContain(
      "Older agents continue working",
    );
  });
  it("rejects mismatched statuses, unknown or duplicate checks and arbitrary diagnostics", () => {
    for (const mutate of [
      (s: HAProxyNodeSetup) => {
        s.checks[0]!.status = "error";
      },
      (s: HAProxyNodeSetup) => {
        s.checks[0]!.code = "private raw response";
      },
      (s: HAProxyNodeSetup) => {
        s.checks[0] = s.checks[1]!;
      },
      (s: HAProxyNodeSetup) => {
        s.checks.push(s.checks[0]!);
      },
      (s: HAProxyNodeSetup) => {
        s.observed_at_ms = Infinity;
      },
    ]) {
      const setup = report();
      mutate(setup);
      expect(validNodeSetup(setup)).toBe(false);
    }
  });
});
