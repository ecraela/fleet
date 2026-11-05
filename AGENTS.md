# Ecraela Fleet repository guidance

- Manager, ingestion, agent, storage and APIs work independently of Converge.
  Customer-owned HAProxy traffic never passes through the management plane.
- HAProxy writes require permissions, local agent policy, verification and audit;
  never add approvals. Converge DNS changes retain its separate review workflow.
- Keep protocol package names and persistent identities compatible across upgrades.
  Missing/corrupt keys fail closed; never re-enroll during recovery.
- Never commit keys, credentials, local state, real customer data or private
  diagnostic responses. Test signers and node states are disposable and private.
- Release manifests require publisher verification and exact version/digests.
  The agent never receives Docker/systemd authority or replaces itself.
- Keep self-hosted operation independent; Converge login/connector are optional.
- Add tenant isolation tests for API data. Keep metrics/log/control paths bounded.
- Run `make format check test build` and the relevant real-demo scenario.
- Record system-wide decisions in docs/adr. Do not select licensing or publish
  source without the product owner's explicit instruction.

- Standalone console source and the included UI components live in `web`. Preserve
  its React Aria controls, theme tokens and editing drawers. Run `make web-install`
  once before checks. Production serves the built assets from Go; never add a
  Converge runtime/workspace/auth dependency or browser credential storage.

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
