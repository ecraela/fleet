import { describe, expect, it } from "vitest";
import {
  applicationDraftErrors,
  normalizedHostname,
  type ApplicationDraft,
} from "./haproxy-application-form";
const draft: ApplicationDraft = {
  id: "",
  name: "Store",
  listener: "public",
  hostname: "SHOP.Example.test.",
  path_prefix: "/shop",
  health_path: "/healthz",
  algorithm: "roundrobin",
  servers: [
    { name: "web", address: "backend.internal", port: "8080", weight: "100" },
  ],
};
describe("application form validation", () => {
  it("matches saved normalization without marking valid fields invalid", () => {
    expect(normalizedHostname(draft.hostname)).toBe("shop.example.test");
    expect(applicationDraftErrors(draft)).toEqual({});
    expect(
      applicationDraftErrors({
        ...draft,
        servers: [{ ...draft.servers[0]!, address: "2001:db8::1" }],
      }),
    ).toEqual({});
  });
  it("identifies each affected control for a multi-field invalid submission", () => {
    const errors = applicationDraftErrors({
      ...draft,
      name: "",
      hostname: "https://shop.example.test",
      path_prefix: "/shop//items",
      health_path: "/health?token=secret",
      servers: [
        {
          name: "web",
          address: "http://backend.internal",
          port: "0",
          weight: "-1",
        },
        {
          name: "web",
          address: "backend.internal",
          port: "70000",
          weight: "0",
        },
      ],
    });
    for (const key of [
      "name",
      "hostname",
      "path_prefix",
      "health_path",
      "servers.0.address",
      "servers.0.port",
      "servers.0.weight",
      "servers.1.name",
      "servers.1.port",
    ])
      expect(errors[key], key).toBeTruthy();
  });
  it("rejects malformed DNS labels and names that exceed the server byte limit", () => {
    for (const hostname of [
      "-store.example.test",
      "store..example.test",
      "store-.example.test",
      "a".repeat(64) + ".test",
    ])
      expect(
        applicationDraftErrors({ ...draft, hostname }).hostname,
      ).toBeTruthy();
    expect(
      applicationDraftErrors({ ...draft, name: "界".repeat(27) }).name,
    ).toContain("too long");
  });
  it("requires one positive server weight while allowing a disabled peer", () => {
    expect(
      applicationDraftErrors({
        ...draft,
        servers: [{ ...draft.servers[0]!, weight: "0" }],
      })["servers.0.weight"],
    ).toContain("positive");
    expect(
      applicationDraftErrors({
        ...draft,
        servers: [
          ...draft.servers,
          { ...draft.servers[0]!, name: "spare", weight: "0" },
        ],
      }),
    ).toEqual({});
  });
});
