#!/usr/bin/env python3
"""Bootstrap and exercise only the isolated loopback Compose deployment."""

import base64
import json
import os
from pathlib import Path
import secrets
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid


def write_private(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as destination:
        destination.write(value)
    path.chmod(0o600)


def prepare(state):
    env_path = state / "compose.env"
    if env_path.exists():
        return
    organization_id = str(uuid.UUID((state / "organization-id").read_text().strip()))
    write_private(
        env_path,
        f"HAPROXY_ORGANIZATION_ID={organization_id}\n"
        f"HAPROXY_DB_PASSWORD={secrets.token_hex(32)}\n",
    )


def prepare_browser(state):
    path = state / "browser-login"
    if path.exists():
        return
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as destination:
        destination.write(base64.b64encode(secrets.token_bytes(32)).decode() + "\n")
    print("Dedicated browser handoff secret saved privately.")


class Client:
    def __init__(self, state):
        self.context = ssl.create_default_context(cafile=state / "pki" / "ca.crt")
        self.token = (state / "admin-token").read_text().strip()
        self.organization = (state / "organization-id").read_text().strip()

    def request(self, path, data=None, *, authorized=True, organization=None, method=None):
        headers = {"Accept": "application/json"}
        if authorized:
            headers["Authorization"] = f"Bearer {self.token}"
            headers["X-Organization-ID"] = organization or self.organization
        encoded = None
        if data is not None:
            encoded = json.dumps(data).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(
            f"https://localhost:18443{path}", data=encoded, headers=headers, method=method
        )
        with urllib.request.urlopen(request, context=self.context, timeout=3) as response:
            raw = response.read()
            return json.loads(raw) if raw else {}


def wait_for(description, operation, seconds=60):
    deadline = time.monotonic() + seconds
    last_error = None
    while time.monotonic() < deadline:
        try:
            value = operation()
            if value:
                return value
        except (OSError, ValueError, urllib.error.URLError) as error:
            last_error = f"{type(error).__name__}: {error}"
        time.sleep(1)
    raise SystemExit(f"Timed out waiting for {description} ({last_error or 'not ready'}).")


def enroll(state):
    client = Client(state)
    wait_for("manager HTTPS health", lambda: client.request("/healthz", authorized=False))
    cluster_file = state / "cluster-id"
    if not cluster_file.exists():
        cluster = client.request("/v1/clusters", {"name": "Local HAProxy demo"})["cluster"]
        write_private(cluster_file, cluster["id"] + "\n")
    cluster_id = str(uuid.UUID(cluster_file.read_text().strip()))
    result = client.request(
        f"/v1/clusters/{cluster_id}/enrollment-tokens", {"ttl_seconds": 600}
    )
    write_private(state / "enrollment-token", result["token"] + "\n")
    print("Local cluster is ready; enrollment credential saved privately.")


def expect_denied(client, **kwargs):
    try:
        client.request("/v1/nodes", **kwargs)
    except urllib.error.HTTPError as error:
        if error.code in (401, 403):
            return
        raise SystemExit(f"Expected access denial, received HTTP {error.code}.") from error
    raise SystemExit("An unauthorized node-list request unexpectedly succeeded.")


def live_snapshot(client, node_id):
    request = urllib.request.Request(
        f"https://localhost:18443/v1/nodes/{node_id}/events",
        headers={
            "Accept": "text/event-stream",
            "Authorization": f"Bearer {client.token}",
            "X-Organization-ID": client.organization,
        },
    )
    with urllib.request.urlopen(request, context=client.context, timeout=8) as response:
        for _ in range(8):
            line = response.readline(2 * 1024 * 1024 + 1)
            if line.startswith(b"data: "):
                result = json.loads(line[6:])
                if (
                    result.get("node_id") != node_id
                    or not result.get("fresh")
                    or not result.get("scrape_ok")
                ):
                    raise SystemExit("Live SSE snapshot did not contain fresh node metrics.")
                return
    raise SystemExit("Live SSE endpoint did not emit a node snapshot.")


def smoke(state):
    client = Client(state)
    expect_denied(client, authorized=False)
    expect_denied(client, organization=str(uuid.uuid4()))
    for _ in range(10):
        with urllib.request.urlopen("http://localhost:18080/", timeout=3) as response:
            if response.status != 200 or b"Converge HAProxy demo" not in response.read():
                raise SystemExit("The request did not reach the HAProxy demo backend.")

    def enrolled_node():
        nodes = client.request("/v1/nodes")["nodes"]
        return next(
            (node for node in nodes if node["name"] == "local-demo" and not node.get("revoked_at")),
            None,
        )

    node = wait_for("agent enrollment", enrolled_node)

    def collected_metrics():
        result = client.request(f"/v1/nodes/{node['id']}/metrics")
        has_haproxy = any(
            series.get("metric", {}).get("__name__", "").startswith("haproxy_")
            for series in result.get("data", {}).get("result", [])
        )
        return result if result.get("fresh") and result.get("scrape_ok") and has_haproxy else None

    wait_for("stored HAProxy metrics through manager API", collected_metrics)
    live_snapshot(client, node["id"])
    print("PASS: real HAProxy traffic, agent enrollment, mTLS ingestion and tenant-scoped metrics.")
    print("PASS: anonymous and other-organization inventory requests are denied.")
    print("Manager: https://localhost:18443; demo traffic: http://localhost:18080/")


def outage(state):
    client = Client(state)
    nodes = client.request("/v1/nodes")["nodes"]
    node = next(node for node in nodes if node["name"] == "local-demo" and not node.get("revoked_at"))
    identity_path = state / "agent" / "identity.json"
    original_identity = identity_path.read_bytes()

    def failures(snapshot):
        return next(
            float(series["value"][1])
            for series in snapshot.get("data", {}).get("result", [])
            if series.get("metric", {}).get("__name__") == "converge_agent_remote_write_failures_total"
        )

    previous = failures(client.request(f"/v1/nodes/{node['id']}/metrics"))
    compose = [
        "docker", "compose", "--project-directory", str(Path(__file__).resolve().parents[1]),
        "--env-file", str(state / "compose.env"),
    ]
    subprocess.run([*compose, "stop", "ingest"], check=True)
    try:
        for _ in range(20):
            with urllib.request.urlopen("http://localhost:18080/", timeout=3) as response:
                if response.status != 200:
                    raise SystemExit("HAProxy stopped serving during ingestion outage.")
            time.sleep(0.5)
    finally:
        subprocess.run([*compose, "start", "ingest"], check=True)

    def recovered():
        snapshot = client.request(f"/v1/nodes/{node['id']}/metrics")
        return snapshot if snapshot.get("fresh") and snapshot.get("scrape_ok") and failures(snapshot) > previous else None

    wait_for("fresh metrics after ingestion recovery", recovered)
    if identity_path.read_bytes() != original_identity:
        raise SystemExit("Agent identity changed during ingestion outage.")
    if len(client.request("/v1/nodes")["nodes"]) != len(nodes):
        raise SystemExit("Ingestion outage unexpectedly enrolled another node.")
    print("PASS: HAProxy served during ingestion outage; agent resumed with its existing identity and recorded export loss.")


def main():
    actions = {"prepare": prepare, "prepare-browser": prepare_browser, "enroll": enroll, "smoke": smoke, "outage": outage}
    if len(sys.argv) != 3 or sys.argv[1] not in actions:
        raise SystemExit("Usage: demo.py {prepare|prepare-browser|enroll|smoke|outage} STATE_DIRECTORY")
    state = Path(sys.argv[2]).resolve()
    actions[sys.argv[1]](state)


if __name__ == "__main__":
    main()
