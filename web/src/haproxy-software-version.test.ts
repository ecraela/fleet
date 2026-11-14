import { describe, expect, it } from "vitest";
import { softwareAction } from "./haproxy-software-version";

describe("Fleet software direction", () => {
  it("uses numeric prerelease ordering for upgrades and restores", () => {
    expect(softwareAction("v0.1.0-rc.9", "v0.1.0-rc.10")).toBe("upgrade");
    expect(softwareAction("v0.1.0-rc.22", "v0.1.0-rc.21")).toBe("rollback");
    expect(softwareAction("v1.0.0-rc.1", "v1.0.0")).toBe("upgrade");
    expect(softwareAction("v1.0.0", "v1.0.0-rc.1")).toBe("rollback");
  });

  it("does not request a change for the current or unknown version", () => {
    expect(softwareAction("v0.1.0-rc.22", "v0.1.0-rc.22")).toBeNull();
    expect(softwareAction(undefined, "v0.1.0-rc.22")).toBeNull();
    expect(softwareAction("v0.1.0-rc.22", "invalid")).toBeNull();
  });
});
