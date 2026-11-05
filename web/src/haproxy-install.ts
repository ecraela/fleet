import type { HAProxyAgentSetup, HAProxySnapshot } from "./haproxy-api";

// Every value, including installation configuration, is a literal shell argument.
export function shellQuote(value: string): string {
  return "'" + value.replaceAll("'", "'\"'\"'") + "'";
}

export function agentInstallCommand(
  setup: HAProxyAgentSetup,
  token: string,
  name: string,
  metrics: string,
  id: string,
  mode: "docker" | "linux" = "docker",
): string {
  if (!/^[a-f0-9-]{36}$/.test(id)) throw new Error("Invalid setup identity");
  if (setup.release_version && setup.release_url)
    return releasedAgentInstallCommand(setup, token, name, metrics, id, mode);
  if (mode === "linux")
    throw new Error("Configure a signed release for Linux installation");
  const q = shellQuote;
  return [
    "(",
    "  set -eu",
    "  umask 077",
    '  cv_agent_dir="${XDG_DATA_HOME:-$HOME/.local/share}/converge-haproxy/' +
      id +
      '"',
    '  mkdir -p "$cv_agent_dir/identity"',
    '  chmod 700 "$cv_agent_dir" "$cv_agent_dir/identity"',
    `  printf '%s\\n' ${q(token)} > "$cv_agent_dir/enrollment-token"`,
    `  printf '%s\\n' ${q(setup.ca_pem)} > "$cv_agent_dir/ca.crt"`,
    `  docker run -d --name ${q("converge-haproxy-" + id)} \\`,
    "    --restart unless-stopped --read-only --cap-drop ALL \\",
    "    --security-opt no-new-privileges:true --memory 96m \\",
    '    --env GOMEMLIMIT=64MiB --user "$(id -u):$(id -g)" \\',
    `    --network ${q(setup.network)} \\`,
    '    --mount "type=bind,src=$cv_agent_dir/ca.crt,dst=/run/haproxy/ca.crt,readonly" \\',
    '    --mount "type=bind,src=$cv_agent_dir/enrollment-token,dst=/run/haproxy/enrollment-token,readonly" \\',
    '    --mount "type=bind,src=$cv_agent_dir/identity,dst=/var/lib/haproxy-agent" \\',
    `    --entrypoint /usr/local/bin/haproxy-agent ${q(setup.image)} \\`,
    `    --server-url ${q(setup.manager_url)} \\`,
    `    --ingest-url ${q(setup.ingest_url)} \\`,
    "    --server-ca /run/haproxy/ca.crt \\",
    "    --enrollment-token-file /run/haproxy/enrollment-token \\",
    `    --state-dir /var/lib/haproxy-agent --name ${q(name)} \\`,
    `    --metrics-url ${q(metrics)} --interval 1s`,
    ")",
  ].join("\n");
}

function releasedAgentInstallCommand(
  setup: HAProxyAgentSetup,
  token: string,
  name: string,
  metrics: string,
  id: string,
  mode: "docker" | "linux",
): string {
  const version = setup.release_version!;
  if (
    !/^v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-z0-9.-]+)?$/.test(version) ||
    setup.release_url !==
      `https://github.com/ecraela/fleet/releases/download/${version}`
  )
    throw new Error("Invalid signed release settings");
  const q = shellQuote;
  const linux = mode === "linux";
  const paths = linux ? "/etc/haproxy-agent" : "/run/haproxy";
  const args = [
    "--server-url",
    setup.manager_url,
    "--ingest-url",
    setup.ingest_url,
    "--server-ca",
    `${paths}/ca.crt`,
    "--enrollment-token-file",
    `${paths}/enrollment-token`,
    "--state-dir",
    "/var/lib/haproxy-agent",
    "--name",
    name,
    "--metrics-url",
    metrics,
    "--interval",
    "1s",
  ];
  const settings = {
    mode,
    arguments: args,
    user: "haproxy-agent",
    network: setup.network,
    image: setup.image,
  };
  return [
    "(",
    "  set -eu",
    "  umask 077",
    ...(linux
      ? [
          '  test "$(id -u)" -eq 0',
          '  if test -e /opt/ecraela-haproxy-agent/deployment.json || test -e /var/lib/haproxy-agent/identity.json || test -e /etc/haproxy-agent/enrollment-token; then echo "An agent is already installed. Use its upgrade profile." >&2; exit 1; fi',
          "  id haproxy-agent >/dev/null 2>&1 || useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin haproxy-agent",
        ]
      : []),
    `  cv_agent_dir="\${XDG_DATA_HOME:-$HOME/.local/share}/ecraela-haproxy/${id}"`,
    '  mkdir -p "$cv_agent_dir/release" "$cv_agent_dir/identity"',
    `  gh release download ${q(version)} --repo ecraela/fleet --dir "$cv_agent_dir/release" --pattern 'release.*' --pattern 'agentctl.py' --pattern 'haproxy-agent-linux-*'`,
    `  cosign verify-blob --bundle "$cv_agent_dir/release/release.sigstore.json" --certificate-identity ${q(`https://github.com/ecraela/fleet/.github/workflows/release.yml@refs/tags/${version}`)} --certificate-oidc-issuer https://token.actions.githubusercontent.com "$cv_agent_dir/release/release.json"`,
    "  python3 -I - \"$cv_agent_dir/release\" <<'PY'",
    "import hashlib, json, pathlib, sys",
    "p = pathlib.Path(sys.argv[1])",
    "m = json.loads((p / 'release.json').read_text())",
    "if hashlib.sha256((p / 'agentctl.py').read_bytes()).hexdigest() != m['artifacts']['agentctl.py']: raise SystemExit('Installer checksum mismatch')",
    "PY",
    ...(linux
      ? ["  install -d -m 750 -o root -g haproxy-agent /etc/haproxy-agent"]
      : []),
    `  printf '%s\\n' ${q(token)} > ${linux ? "/etc/haproxy-agent/enrollment-token" : '"$cv_agent_dir/enrollment-token"'}`,
    `  printf '%s\\n' ${q(setup.ca_pem)} > ${linux ? "/etc/haproxy-agent/ca.crt" : '"$cv_agent_dir/ca.crt"'}`,
    ...(linux
      ? [
          "  chown haproxy-agent:haproxy-agent /etc/haproxy-agent/enrollment-token /etc/haproxy-agent/ca.crt",
        ]
      : []),
    '  python3 -I - "$cv_agent_dir" "$(id -u):$(id -g)" <<\'PY\'',
    "import json, pathlib, sys",
    "p = pathlib.Path(sys.argv[1])",
    `c = json.loads(${JSON.stringify(JSON.stringify(settings))})`,
    `c['state_dir'] = ${linux ? "'/var/lib/haproxy-agent'" : "str(p / 'identity')"}`,
    `c['install_dir'] = ${linux ? "'/opt/ecraela-haproxy-agent'" : "str(p / 'software')"}`,
    ...(!linux
      ? [
          "service = {'image': c.pop('image'), 'entrypoint': ['/usr/local/bin/haproxy-agent'], 'command': c.pop('arguments'), 'network_mode': c.pop('network'), 'user': sys.argv[2], 'read_only': True, 'cap_drop': ['ALL'], 'security_opt': ['no-new-privileges:true'], 'restart': 'unless-stopped', 'mem_limit': '96m', 'environment': {'GOMEMLIMIT': '64MiB'}, 'volumes': [str(p / 'identity') + ':/var/lib/haproxy-agent', str(p / 'ca.crt') + ':/run/haproxy/ca.crt:ro', str(p / 'enrollment-token') + ':/run/haproxy/enrollment-token:ro']}",
          `compose = {'name': ${JSON.stringify("haproxy-" + id)}, 'services': {'agent': service}}`,
          "(p / 'compose.json').write_text(json.dumps(compose))",
          "c['docker'] = {'project_directory': str(p), 'files': [str(p / 'compose.json')], 'service': 'agent'}",
        ]
      : ["c.pop('image'); c.pop('network')"]),
    "(p / 'agentctl.json').write_text(json.dumps(c))",
    "PY",
    `  python3 -I "$cv_agent_dir/release/agentctl.py" install --config "$cv_agent_dir/agentctl.json" --release "$cv_agent_dir/release" --version ${q(version)}`,
    ")",
  ].join("\n");
}

export function hasReportingMetrics(
  snapshot: HAProxySnapshot,
  nodeID: string,
  now = Date.now(),
): boolean {
  const observed = Date.parse(snapshot.observed_at ?? "");
  return (
    snapshot.node_id === nodeID &&
    snapshot.fresh &&
    snapshot.scrape_ok &&
    Number.isFinite(observed) &&
    now - observed <= 15_000 &&
    observed - now <= 60_000 &&
    snapshot.data?.result.some(
      (series) =>
        series.metric.__name__?.startsWith("haproxy_") &&
        series.metric.node_id === nodeID &&
        Number.isFinite(Number(series.value[1])),
    ) === true
  );
}
