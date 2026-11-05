# Connect a self-hosted manager to Converge

The manager and its local console work without Converge. The optional connector
opens an outbound TLS connection to a separately deployed Go relay. Your agents
continue reporting to your manager; HAProxy traffic and private keys stay on
your infrastructure.

```text
Converge console ── HTTPS, short cloud login ── Go relay
       │                                       ║
       └── Phoenix: identity/permissions        ║ existing outbound stream
                                               ║ initiated by your manager
Local console ── local login ── Your Go manager ═╝
                                │         │
                              agent A   agent B
                                │         │
                             HAProxy A  HAProxy B
```

## Deployment

Deploy the same manager executable with `serve --role relay`, an installation
PKI/TLS certificate, a dedicated HAProxy database and these settings:

```sh
haproxy-manager serve --role relay --listen :8443 --pki-dir /var/lib/relay/pki \
  --browser-origin https://converge.example.com \
  --browser-login-secret-file /run/secrets/converge-haproxy-login
```

Set `HAPROXY_DATABASE_URL` from your secret manager, run `haproxy-manager migrate`
as the database migration principal, and configure Phoenix with that same
dedicated browser-login secret and audience. Point Converge's
`HAPROXY_API_ORIGIN` at the relay's HTTPS origin. The local Vite equivalent is
`VITE_HAPROXY_PROXY_TARGET`; `HAPROXY_DEV_CA` supplies a private development CA.
These dev settings may live in the console's ignored `.env.local` file.

The relay's public ingress must support HTTP/2 gRPC at
`/ecraela.haproxy.connector.v1.Relay/Connect` and the bounded
`/v1/connector/claim` machine endpoints. Converge's browser proxy exposes only
`/v1/connector` settings and the normal browser API paths. It excludes claim,
machine authentication, local login and the gRPC transport. Use verified TLS
end to end. The relay does not need the manager's address, PKI or operator key.

Add these optional flags to your existing self-hosted manager command:

```sh
--connector-relay-url https://relay.example.com \
--connector-state-dir /var/lib/haproxy-manager/connector
```

For installations managed by `selfhostctl.py`, use the released host tool:

```sh
python3 -I selfhostctl.py connector-configure --directory /srv/haproxy-manager \
  --connector-relay-url https://relay.example.com
```

Add `--connector-ca-file /path/to/relay-ca.crt` for a private relay CA. The tool
copies public trust into installation state and mounts a private connector
directory for the manager. Upgrades, software rollback and encrypted restore
retain the destination, trust and connector identity. This configures the
destination; pairing still requires a local administrator. Unlink before
changing a paired relay. Local login and node identities remain independent.

The equivalent environment variables are `HAPROXY_CONNECTOR_RELAY_URL` and
`HAPROXY_CONNECTOR_STATE_DIR`. For an installation using a private relay CA, set
`--connector-ca-file` / `HAPROXY_CONNECTOR_CA_FILE` to its PEM trust bundle.
System trust roots are used otherwise. There is no insecure TLS option. The
origin is configured by the host administrator and cannot be changed by a
browser pairing code. Keep the directory on a persistent writable volume,
mode `0700`, owned by the manager runtime user. Keep the rest of the container
read-only. Back up this directory privately alongside installation state; it
contains a connector credential, not node private keys. Do not share its volume
between concurrently running managers.

Keep local login enabled with the existing `--local-origin` and account setup.
Permit local UI access from the operator network. No manager port must be
publicly reachable by Converge. Nodes still need their normal private access to
their manager and ingestion endpoint.

## Link and unlink

1. In the Converge organization's HAProxy page, choose **Connect manager**.
2. Enter a manager name and create a linking code. This requires Manage HAProxy
   permission and Converge's existing management authentication check.
3. Sign in locally as an administrator. Open **Converge connection** and paste
   the code within ten minutes. Choose whether to allow management or observation
   only, then link. The current local account fixes the local organization.
4. Both consoles confirm **Connected**. Converge shows the same fleet, stored
   charts, applications and authorized controls through the outbound stream.

Codes are shown once; lost/expired codes can be unlinked and recreated. Codes,
cookies and credentials are never placed in URLs or browser storage. A local
viewer can inspect connection status but cannot link or unlink. A Converge
viewer can observe but cannot create/revoke links or make changes. Local
observation-only policy also rejects writes from cloud administrators.

Use **Unlink manager** in Converge to revoke remote access, or **Unlink Converge**
in the local console to stop it immediately. Local login, applications and agent
identities remain. If the relay is unavailable during local unlink, remove the
orphaned cloud link later. To change the fixed relay URL, unlink before updating
the host configuration. Do not delete or regenerate a damaged identity during
upgrade/recovery; restore the private state or explicitly revoke and relink.

If a change times out, inspect its operation/version history before resubmitting.
The connector never replays an uncertain mutation. DNS controls still prepare
ordinary Converge DNS drafts; review/apply stays in the DNS workflow. HAProxy
operations do not enter that review process.

## Two-node demo

Build the current Docker image as `ecraela-haproxy-connector:local`. Prepare the
two-node and standalone account fixtures first. For an existing Converge
checkout's preserved demo state:

```sh
python3 scripts/connector.py up --demo-root /path/to/converge/apps/haproxy_manager
```

This replaces only the optional standalone demo manager container and adds a
relay with a separate HAProxy database. The local console stays at
`http://localhost:5180`; the relay is `https://localhost:18446`. It prints the
development CA path. Configure the Converge console proxy accordingly and link
from the normal console flow. The manager API has no published port and listens
only on its own container loopback; its local console remains published on
host loopback. Original node/agent containers, volumes, accounts and keys stay.

After linking, run the repeatable outage/recovery scenario:

```sh
python3 scripts/connector.py smoke --demo-root /path/to/converge/apps/haproxy_manager
```

It verifies both apps over HTTP and trusted HTTPS while stopping/restarting the
relay and restarting the manager. It checks local login/metrics during cloud
failure, reconnection, preserved local session, connector identity and node keys,
and refusal of an inbound manager API connection from the relay. Sanitized
results go to the ignored `standalone-console/outbound-lab/latest.json`. No DNS
provider writes, certificate/key replacements or application configuration
changes are made. `scripts/connector.py status` prints only bounded connection
state. `link --code-file PRIVATE_FILE` is available for scripted local fixture
linking without exposing the code in process arguments or command output.

The pilot supports one manager per organization and one relay replica. Do not
scale out relay replicas until connection routing/ownership is implemented.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
