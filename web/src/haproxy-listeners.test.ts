import { describe, expect, it } from "vitest";
import { sharedCertificates, httpsListenerDraft } from "./haproxy-listeners";
import {
  validApplication,
  validConfigurationDocument,
  validHTTPSListeners,
  type HAProxyConfigurationView,
  type HAProxyTLSCertificate,
} from "./haproxy-api";

const cert: HAProxyTLSCertificate = {
  reference: "first",
  sha256: "a".repeat(64),
  dns_names: ["*.example.test"],
  not_before_ms: 1,
  not_after_ms: 1000000,
  error_code: "",
};
const view = (c: HAProxyTLSCertificate): HAProxyConfigurationView => ({
  control: {
    node_id: "node",
    connected_at: "",
    last_seen: "",
    discovery: {
      configuration: {
        tls_certificates: [c],
        version: 1,
        artifact_id: "a".repeat(64),
        observed_at_ms: 1,
        control_allowed: true,
        error_code: "",
      },
      version: "3.4",
      process_id: "1",
      started_at: 1,
      config_fingerprint: "",
      generation: "",
      control_allowed: true,
      observed_at_ms: 1,
      error_code: "",
      config_files: 1,
      proxies: [],
      servers: [],
    },
  },
  connected: true,
  fresh: true,
  configuration_enabled: true,
  versions: [],
});
describe("HTTPS listener admission hints", () => {
  it("requires the same valid leaf on all nodes and rejects expired or missing material", () => {
    expect(
      sharedCertificates([view(cert), view(cert)], 1000)[0]?.reason,
    ).toBeUndefined();
    for (const other of [
      { ...cert, sha256: "b".repeat(64) },
      { ...cert, error_code: "certificate_invalid" },
      { ...cert, not_after_ms: 1000 },
      { ...cert, not_before_ms: 2000 },
    ])
      expect(
        sharedCertificates([view(cert), view(other)], 1000)[0]?.reason,
      ).toBeTruthy();
    expect(
      sharedCertificates(
        [
          view(cert),
          {
            ...view(cert),
            control: { ...view(cert).control, discovery: null },
          },
        ],
        1000,
      )[0]?.reason,
    ).toBeTruthy();
  });
  it("bounds listener intent and allows incoming ports only on infrastructure backends", () => {
    const l = {
      name: "public",
      address: "*",
      port: 8443,
      certificate_ref: "first",
      certificate_sha256: cert.sha256,
    };
    expect(validHTTPSListeners([l])).toBe(true);
    expect(validHTTPSListeners([l, l])).toBe(false);
    expect(validHTTPSListeners([{ ...l, port: 0 }])).toBe(false);
    const server = {
      name: "one",
      address: "node",
      port: 0,
      forward_incoming_port: true,
      weight: 100,
      maintenance: false,
    };
    expect(
      validConfigurationDocument({
        backends: [
          { name: "nodes", algorithm: "roundrobin", servers: [server] },
        ],
        listeners: [l],
      }),
    ).toBe(true);
    expect(
      validConfigurationDocument({
        backends: [
          {
            name: "nodes",
            algorithm: "roundrobin",
            servers: [{ ...server, forward_incoming_port: false }],
          },
        ],
      }),
    ).toBe(false);
    expect(
      validApplication({
        id: "id",
        name: "App",
        listener: "public",
        hostname: "apps.example.test",
        path_prefix: "/",
        health_path: "/healthz",
        algorithm: "roundrobin",
        servers: [server],
      }),
    ).toBe(false);
  });
});

it("uses the HTTPS draft state only when building the submitted listener intent", () => {
  const binding = { name: "public", address: "*", port: 8443 };
  expect(httpsListenerDraft(false, undefined, undefined)).toEqual([]);
  expect(httpsListenerDraft(true, undefined, cert)).toBeNull();
  expect(httpsListenerDraft(true, binding, undefined)).toBeNull();
  expect(
    httpsListenerDraft(true, binding, { ...cert, reason: "Unavailable" }),
  ).toBeNull();
  expect(httpsListenerDraft(true, binding, cert)).toEqual([
    {
      ...binding,
      certificate_ref: cert.reference,
      certificate_sha256: cert.sha256,
    },
  ]);
  const pins = { "00000000-0000-4000-8000-000000000001": "b".repeat(64) };
  expect(
    httpsListenerDraft(true, binding, { ...cert, node_certificates: pins }),
  ).toEqual([
    {
      ...binding,
      certificate_ref: cert.reference,
      certificate_sha256: "",
      node_certificates: pins,
    },
  ]);
});
