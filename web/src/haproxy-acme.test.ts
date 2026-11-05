import { describe, expect, it } from "vitest";
import {
  validACMEView,
  validHTTPSListeners,
  type HAProxyACMEView,
  type HAProxyConfigurationView,
} from "./haproxy-api";
import { sharedCertificates } from "./haproxy-listeners";
import { acmeError, acmeStatus, acmePrimaryAction } from "./haproxy-acme";
const id = (n: number) =>
  `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
function state(): HAProxyACMEView {
  const certificate = {
    reference: `acme-${"a".repeat(40)}`,
    sha256: "c".repeat(64),
    dns_names: ["apps.example.test"],
    not_before_ms: 1,
    not_after_ms: 1000000,
    error_code: "",
  };
  return {
    options: [
      {
        issuer: "local-ca",
        environment: "staging",
        binding: { name: "public", address: "*", port: 8443 },
        challenges: [
          { hostname: "apps.example.test", target: "app.validation.test" },
        ],
      },
    ],
    plan: {
      id: id(1),
      organization_id: id(2),
      cluster_id: id(3),
      version: 1,
      enabled: true,
      issuer: "local-ca",
      environment: "staging",
      binding: { name: "public", address: "*", port: 8443 },
      hostnames: ["apps.example.test"],
      challenges: [
        { hostname: "apps.example.test", target: "app.validation.test" },
      ],
      generation: 1,
      status: "issuing",
      error_code: "",
      jobs: [4, 5].map((n) => ({
        id: id(n + 10),
        plan_id: id(1),
        generation: 1,
        node_id: id(n),
        issuer: "local-ca",
        listener: "public",
        hostnames: ["apps.example.test"],
        policy_digest: "a".repeat(64),
        reference: certificate.reference,
        status: "ready",
        error_code: "",
        certificate: {
          ...certificate,
          sha256: (n === 4 ? "c" : "d").repeat(64),
        },
        attempts: 1,
        next_try_ms: 0,
        deadline_ms: 1000000,
      })),
      history: [],
      renew_at_ms: 0,
      next_run_ms: 1000,
    },
  };
}
describe("automatic TLS authority", () => {
  it("binds public issuance metadata to the organization, cluster and exact generation", () => {
    const s = state();
    expect(validACMEView(s, id(2), id(3))).toBe(true);
    expect(validACMEView(s, id(8), id(3))).toBe(false);
    expect(validACMEView(s, id(2), id(8))).toBe(false);
    for (const change of [
      (v: HAProxyACMEView) => {
        v.plan!.jobs[0]!.plan_id = id(9);
      },
      (v: HAProxyACMEView) => {
        v.plan!.jobs[0]!.generation = 2;
      },
      (v: HAProxyACMEView) => {
        v.plan!.jobs[0]!.node_id = v.plan!.jobs[1]!.node_id;
      },
      (v: HAProxyACMEView) => {
        v.plan!.jobs[0]!.certificate!.reference = "other";
      },
      (v: HAProxyACMEView) => {
        v.options[0]!.challenges[0]!.target = "https://attacker.test";
      },
    ]) {
      const v = structuredClone(s);
      change(v);
      expect(validACMEView(v, id(2), id(3))).toBe(false);
    }
  });
  it("preserves separate leaf identities for the same immutable generation on different nodes", () => {
    const s = state(),
      jobs = s.plan!.jobs;
    const views = jobs.map(
      (j) =>
        ({
          control: {
            node_id: j.node_id,
            discovery: { configuration: { tls_certificates: [j.certificate] } },
          },
        }) as unknown as HAProxyConfigurationView,
    );
    const c = sharedCertificates(views, 1000)[0]!;
    expect(c.reason).toBeUndefined();
    expect(c.node_certificates).toEqual({
      [id(4)]: "c".repeat(64),
      [id(5)]: "d".repeat(64),
    });
    const l = {
      name: "public",
      address: "*",
      port: 8443,
      certificate_ref: c.reference,
      certificate_sha256: "",
      node_certificates: c.node_certificates!,
    };
    expect(validHTTPSListeners([l])).toBe(true);
    expect(
      validHTTPSListeners([{ ...l, certificate_sha256: "a".repeat(64) }]),
    ).toBe(false);
    expect(
      validHTTPSListeners([
        { ...l, node_certificates: { "not-a-node": "a".repeat(64) } },
      ]),
    ).toBe(false);
  });
  it("explains failed issuance and keeps review language specific to DNS delegation", () => {
    expect(acmeError("delegation_missing")).toContain("DNS review");
    expect(acmeError("activation_failed")).toContain("Rolling configuration");
    expect(acmeStatus("deploying")).toBe("Rolling HTTPS update");
    expect(acmeError("acme_rate_limited")).toContain("retry delay");
  });
});

describe("certificate actions", () => {
  it("offers one action for the actual certificate state", () => {
    const plan = state().plan!;
    expect(acmePrimaryAction(null).action).toBe("enable");
    expect(
      acmePrimaryAction({ ...plan, enabled: false, status: "disabled" }).action,
    ).toBe("enable");
    expect(acmePrimaryAction({ ...plan, status: "active" }).action).toBe(
      "renew",
    );
    expect(
      acmePrimaryAction({
        ...plan,
        status: "error",
        error_code: "acme_unavailable",
      }).action,
    ).toBe("retry");
    for (const status of ["issuing", "ready", "deploying"] as const)
      expect(acmePrimaryAction({ ...plan, status }).action).toBeNull();
    expect(
      acmePrimaryAction({ ...plan, enabled: false, status: "deploying" })
        .action,
    ).toBeNull();
  });
  it("preserves recovery gates and certificate-authority retry delays", () => {
    const plan = { ...state().plan!, status: "error" as const };
    for (const code of ["activation_failed", "authority_changed"])
      expect(
        acmePrimaryAction({ ...plan, error_code: code }).disabledReason,
      ).toContain("Resolve recovery");
    const delayed = {
      ...plan,
      jobs: [
        { ...plan.jobs[0]!, status: "failed" as const, next_try_ms: 2000 },
      ],
    };
    expect(acmePrimaryAction(delayed, 1000).disabledReason).toContain(
      "retry delay",
    );
    expect(acmePrimaryAction(delayed, 2001).disabledReason).toBeUndefined();
  });
});
