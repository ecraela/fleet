import { describe, expect, it } from "vitest";
import { createHash } from "node:crypto";
import {
  mkdtempSync,
  writeFileSync,
  readFileSync,
  existsSync,
  statSync,
  rmSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import {
  agentInstallCommand,
  hasReportingMetrics,
  shellQuote,
} from "./haproxy-install";
import type { HAProxyAgentSetup, HAProxySnapshot } from "./haproxy-api";

const setup: HAProxyAgentSetup = {
  image: "example.test/agent:1",
  manager_url: "https://manager.test",
  ingest_url: "https://ingest.test",
  network: "host",
  metrics_url: "http://127.0.0.1:8405/metrics",
  ca_pem: "PUBLIC CA\nsecond line\n",
};
const id = "5b7037a9-5778-491c-ab6b-a17624b4901c";

describe("guided agent install command", () => {
  it("runs as a shell script, keeps credentials in private files, and quotes hostile names literally", () => {
    const root = mkdtempSync(join(tmpdir(), "cv-agent-command-"));
    try {
      const output = join(root, "arguments");
      writeFileSync(
        join(root, "docker"),
        '#!/bin/sh\nprintf "%s\\n" "$@" > "$CV_TEST_DOCKER_ARGS"\n',
        { mode: 0o700 },
      );
      const marker = join(root, "injected");
      const name = `edge'; touch ${marker}; echo ' $(touch ${marker})`;
      const token = "synthetic-one-use-credential-0123456789";
      const script = agentInstallCommand(
        setup,
        token,
        name,
        setup.metrics_url,
        id,
      );
      execFileSync("/bin/sh", ["-n"], { input: script });
      execFileSync("/bin/sh", [], {
        input: script,
        env: {
          ...process.env,
          PATH: root + ":" + process.env.PATH,
          XDG_DATA_HOME: root,
          CV_TEST_DOCKER_ARGS: output,
        },
      });
      const dir = join(root, "converge-haproxy", id);
      expect(readFileSync(join(dir, "enrollment-token"), "utf8")).toBe(
        token + "\n",
      );
      expect(readFileSync(join(dir, "ca.crt"), "utf8")).toBe(
        setup.ca_pem + "\n",
      );
      expect(statSync(join(dir, "enrollment-token")).mode & 0o777).toBe(0o600);
      expect(statSync(join(dir, "identity")).mode & 0o777).toBe(0o700);
      const args = readFileSync(output, "utf8");
      expect(args).toContain(name);
      expect(args).not.toContain(token);
      expect(args).toContain("/usr/local/bin/haproxy-agent");
      expect(existsSync(marker)).toBe(false);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
  it("rejects an unsafe installation directory identifier", () => {
    expect(() =>
      agentInstallCommand(
        setup,
        "credential",
        "edge",
        setup.metrics_url,
        "../../escape",
      ),
    ).toThrow();
  });
  it("verifies the exact publisher and installer before a pinned Docker install, preserving literal arguments", () => {
    const root = mkdtempSync(join(tmpdir(), "cv-signed-agent-"));
    try {
      const version = "v0.1.0";
      const signed = {
        ...setup,
        image: "ghcr.io/ecraela/fleet@sha256:" + "a".repeat(64),
        release_version: version,
        release_url: `https://github.com/ecraela/fleet/releases/download/${version}`,
      };
      const marker = join(root, "injected");
      const name = `edge'; $(touch ${marker}) \"quoted\"`;
      const installer =
        "import pathlib, sys\npathlib.Path(sys.argv[sys.argv.index('--config') + 1]).with_name('installer-ran').write_text('yes')\n";
      const manifest = JSON.stringify({
        artifacts: {
          "agentctl.py": createHash("sha256").update(installer).digest("hex"),
        },
      });
      writeFileSync(
        join(root, "curl"),
        `#!/bin/sh\nset -eu\nprintf '%s\\n' "$@" >> "$CV_TEST_DOWNLOADS"\nwhile [ "$1" != --output ]; do shift; done\nshift\ncase "$1" in\n*/release.json) printf '%s' ${shellQuote(manifest)} > "$1";;\n*/agentctl.py) printf '%s' ${shellQuote(installer)} > "$1";;\n*) printf '%s' '{}' > "$1";;\nesac\n`,
        { mode: 0o700 },
      );
      writeFileSync(join(root, "gh"), "#!/bin/sh\nexit 99\n", {
        mode: 0o700,
      });
      writeFileSync(
        join(root, "cosign"),
        '#!/bin/sh\nprintf \'%s\\n\' "$@" > "$CV_TEST_VERIFY_ARGS"\n',
        { mode: 0o700 },
      );
      const script = agentInstallCommand(
        signed,
        "synthetic-token",
        name,
        setup.metrics_url,
        id,
      );
      execFileSync("/bin/sh", ["-n"], { input: script });
      execFileSync("/bin/sh", [], {
        input: script,
        env: {
          ...process.env,
          PATH: root + ":" + process.env.PATH,
          XDG_DATA_HOME: root,
          CV_TEST_VERIFY_ARGS: join(root, "verification"),
          CV_TEST_DOWNLOADS: join(root, "downloads"),
        },
      });
      const folder = join(root, "ecraela-haproxy", id);
      const compose = JSON.parse(
        readFileSync(join(folder, "compose.json"), "utf8"),
      );
      expect(compose.services.agent.command).toContain(name);
      expect(compose.services.agent.command).not.toContain("synthetic-token");
      expect(compose.services.agent.image).toBe(signed.image);
      expect(readFileSync(join(root, "verification"), "utf8")).toContain(
        `.github/workflows/release.yml@refs/tags/${version}`,
      );
      expect(existsSync(join(folder, "installer-ran"))).toBe(true);
      expect(script).not.toContain("gh release download");
      const downloads = readFileSync(join(root, "downloads"), "utf8");
      expect(downloads).toContain(signed.release_url + "/agentctl.py");
      expect(downloads).not.toContain("Authorization");
      expect(existsSync(marker)).toBe(false);
      expect(statSync(join(folder, "agentctl.json")).mode & 0o777).toBe(0o600);
      const linux = agentInstallCommand(
        signed,
        "synthetic-token",
        name,
        setup.metrics_url,
        id,
        "linux",
      );
      execFileSync("/bin/sh", ["-n"], { input: linux });
      expect(linux).toContain("/var/lib/haproxy-agent");
      expect(linux).toContain("useradd --system");
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
  it("rejects other publishers, mutable images and lookalike release hosts", () => {
    const signed = {
      ...setup,
      release_version: "v0.1.0",
      release_url: "https://github.com/ecraela/fleet/releases/download/v0.1.0",
      image: "ghcr.io/ecraela/fleet@sha256:" + "a".repeat(64),
    };
    for (const change of [
      { image: "ghcr.io/ecraela/haproxy-manager@sha256:" + "a".repeat(64) },
      { image: "ghcr.io/ecraela/fleet:v0.1.0" },
      {
        release_url:
          "https://github.com.attacker.test/ecraela/fleet/releases/download/v0.1.0",
      },
    ]) {
      for (const mode of ["docker", "linux"] as const) {
        expect(() =>
          agentInstallCommand(
            { ...signed, ...change },
            "synthetic-token",
            "edge",
            setup.metrics_url,
            id,
            mode,
          ),
        ).toThrow("Invalid signed release settings");
      }
    }
  });
});

describe("reporting confirmation", () => {
  const now = Date.parse("2026-10-24T12:00:00Z");
  const sample: HAProxySnapshot = {
    node_id: "node-a",
    observed_at: new Date(now).toISOString(),
    fresh: true,
    scrape_ok: true,
    data: {
      resultType: "vector",
      result: [
        {
          metric: {
            __name__: "haproxy_frontend_bytes_in_total",
            node_id: "node-a",
          },
          value: [now / 1000, "42"],
        },
      ],
    },
  };
  it("requires fresh HAProxy data from the enrolled node", () => {
    expect(hasReportingMetrics(sample, "node-a", now)).toBe(true);
    expect(hasReportingMetrics(sample, "node-b", now)).toBe(false);
    expect(
      hasReportingMetrics({ ...sample, scrape_ok: false }, "node-a", now),
    ).toBe(false);
    expect(
      hasReportingMetrics({ ...sample, fresh: false }, "node-a", now),
    ).toBe(false);
    expect(hasReportingMetrics(sample, "node-a", now + 16000)).toBe(false);
    expect(
      hasReportingMetrics(
        { ...sample, data: { resultType: "vector", result: [] } },
        "node-a",
        now,
      ),
    ).toBe(false);
    expect(
      hasReportingMetrics(
        {
          ...sample,
          data: {
            resultType: "vector",
            result: [
              {
                metric: {
                  __name__: "converge_agent_haproxy_up",
                  node_id: "node-a",
                },
                value: [0, "1"],
              },
            ],
          },
        },
        "node-a",
        now,
      ),
    ).toBe(false);
  });
});
