# Connect existing HAProxy hosts

Install the standalone manager alongside your infrastructure using the
[self-hosted installation runbook](self-hosted-installation.md). Open its console
and create a cluster. Enroll one agent per existing HAProxy instance.
Your application servers do not need agents. Nodes need private access to the
manager and ingestion endpoints; Converge is optional.

The signed installation command starts with monitoring. Use **Node setup** in
either console to inspect reported access and open **Setup guide** for local
management prerequisites. A green monitoring check does not enable edits.
Older released agents continue operating and show that diagnostics require an
upgrade; this feature is available in builds containing ADR 0051.

## Check the actual agent environment

Before enrollment, run the installed candidate's read-only check as the service
user. For Docker, use the intended network, user and explicit read-only mounts.
Do not mount the Docker socket. For systemd, check as the agent user, and also
confirm the service sandbox allows the paths. A root shell has different access.

```sh
haproxy-agent preflight \
  --metrics-url http://127.0.0.1:8405/metrics
```

To discover existing configuration, add the local Runtime API socket and each
explicit configuration file:

```sh
haproxy-agent preflight \
  --metrics-url http://127.0.0.1:8405/metrics \
  --runtime-socket /run/haproxy/admin.sock \
  --config-file /etc/haproxy/haproxy.cfg \
  --configuration-owner external
```

The JSON report contains fixed diagnostic codes, never paths, raw configuration
or credentials. Exit status 0 means configured checks passed; intentionally
disabled management and external ownership are allowed. Failed metrics, requested
unavailable capabilities or unreadable explicitly configured files return status 1.
Invalid settings fail before probes. No token is required or consumed and no
identity or rollback artifacts are created.

If a Data Plane API socket is explicitly configured, its read access and
configuration compatibility are checked even when persistent writes are disabled.

## Enable only the management you need

For backend drain/restore/weights, add `--allow-runtime-control` to the existing
agent service alongside its runtime socket and configuration files. HAProxy 3.4
and a local admin-level Runtime socket are required for this supported profile.
External configuration ownership may remain declared. Runtime changes are
temporary and may be replaced by the owner's next reload.

Before persistent edits, identify and resolve any GitOps/Ansible/ingress
controller or other writer. Configure the private Data Plane API 3.4 according to
the [supported profile](providers/haproxy-dataplane.md). It must validate and
reload this instance's configuration. Grant its socket and private credentials
to the agent, then add:

```text
--configuration-owner manager
--dataplane-socket /run/haproxy/dataplane.sock
--dataplane-credentials-file /etc/haproxy-agent/dataplane.json
--allow-configuration-control
```

Keep all previous identity, endpoint, runtime socket and configuration file
arguments. Check with `preflight` using the complete settings, then restart only
the agent. Confirm fresh diagnostics before editing in the console. Existing
TLS/probe/request policies remain explicit. HAProxy operations still use direct
permissions, verification and history, without approvals.

Automation comments in the first 16 KiB of each explicit file are hints.
They block persistent commands until manager ownership is declared locally.
They cannot identify every external writer or fence one. Unsupported configuration
stays outside the editing profile; monitoring remains independent.

## Repeat the customer-style pilot

```sh
make onboarding
```

This creates a separate random Docker project, starts existing application traffic
before management, enrolls both nodes, checks restrictive permissions and verifies
traffic/configuration/process/identity preservation. It uses its own PostgreSQL,
VictoriaMetrics, local account and PKI. It never uses Converge or the running demo.
Only sanitized measurements are retained under
`.build.local/onboarding/<run>/latest.json`; private fixture state stays ignored.

Local qualification on 2026-10-28 completed 908 HTTP requests without failures
across onboarding, agent restarts and manager outages. Both applications served
through both nodes; proxy processes, configuration and identities were preserved.
The existing two-node demo separately passed 80 HTTP/HTTPS requests using its
current configured routes. This is a bounded functional check on a Linux Docker
VM, not a capacity benchmark or an additional host-distribution qualification.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
