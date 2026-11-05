import { describe, expect, it } from "vitest";
import {
  applicationSpec,
  applicationEditSource,
  applicationLifecycle,
  latestApplications,
  type ApplicationDraft,
} from "./haproxy-applications";
import {
  validConfigurationDocument,
  type HAProxyApplicationRevision,
} from "./haproxy-api";

const draft: ApplicationDraft = {
  id: "",
  name: " Store ",
  listener: "public",
  hostname: "SHOP.Example.test.",
  path_prefix: "/shop",
  health_path: "/healthz",
  algorithm: "roundrobin",
  servers: [
    { name: "web", address: "backend.internal", port: "8080", weight: "100" },
  ],
};
describe("HTTP applications", () => {
  it("normalizes intent and retains other apps when composing safe documents", () => {
    const spec = applicationSpec(draft);
    expect(spec.hostname).toBe("shop.example.test");
    expect(spec.name).toBe("Store");
    expect(spec.servers[0]?.port).toBe(8080);
    expect(
      validConfigurationDocument({ backends: [], applications: [spec] }),
    ).toBe(true);
    expect(
      validConfigurationDocument({
        backends: [],
        applications: [{ ...spec, health_path: "/healthz\nbackend injected" }],
      }),
    ).toBe(false);
    expect(() =>
      applicationSpec({
        ...draft,
        servers: [{ ...draft.servers[0]!, port: "NaN" }],
      }),
    ).toThrow();
    expect(() =>
      applicationSpec({
        ...draft,
        servers: [{ ...draft.servers[0]!, weight: "0" }],
      }),
    ).toThrow();
    expect(() =>
      applicationSpec({
        ...draft,
        servers: [...draft.servers, ...draft.servers],
      }),
    ).toThrow();
  });
  it("selects the latest revision per app independently of history order or outcome", () => {
    const revision = (
      app: string,
      number: number,
      status: HAProxyApplicationRevision["status"],
    ): HAProxyApplicationRevision => ({
      id: `${app}-${number}`,
      application_id: app,
      number,
      status,
      spec: { ...applicationSpec(draft), id: app, name: app },
      organization_id: "org",
      cluster_id: "cluster",
      request_id: "request",
      rollout_id: "rollout",
      created_at: "2026-10-25T12:00:00Z",
    });
    const rows = [
      revision("Store", 1, "succeeded"),
      revision("Admin", 2, "succeeded"),
      revision("Store", 3, "rolled_back"),
      revision("Admin", 1, "succeeded"),
    ];
    expect(applicationEditSource(rows[2]!, rows).number).toBe(1);
    expect(applicationEditSource(rows[1]!, rows).number).toBe(2);
    const deleted = {
      ...rows[0]!,
      number: 4,
      deleted: true,
      status: "succeeded" as const,
    };
    const failedRestore = {
      ...rows[0]!,
      number: 5,
      status: "rolled_back" as const,
    };
    expect(applicationLifecycle(deleted)).toBe("deleted");
    expect(
      applicationLifecycle({
        ...rows[0]!,
        spec: { ...rows[0]!.spec, disabled: true },
      }),
    ).toBe("disabled");
    expect(
      applicationLifecycle(
        applicationEditSource(failedRestore, [...rows, deleted, failedRestore]),
      ),
    ).toBe("deleted");
    expect(
      latestApplications(rows).map((r) => [r.spec.name, r.number, r.status]),
    ).toEqual([
      ["Admin", 2, "succeeded"],
      ["Store", 3, "rolled_back"],
    ]);
  });
});
