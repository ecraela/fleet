# ADR 0053 — Latest observations for live metrics

Status: accepted

## Decision

Keep the latest two validated metric summaries per enrolled node in HAProxy
Manager's PostgreSQL database. Use them for the live cluster feed. VictoriaMetrics
continues to store and query historical numerical observations.

The pinned VictoriaMetrics release buffers rows before making them searchable.
Its two-second pending-row interval can leave a low-volume shard waiting until
the following tick. One-second collection and a 500 ms console feed can therefore
cross the five-second traffic-to-console target. The disk-flush flag controls
crash persistence and does not remove this searchable-buffer delay. Regular
forced flushes are a diagnostic mechanism, not a production live-data path.

## Boundaries

The existing mTLS ingestion handler validates identity, scope, timestamps,
cardinality and the Remote Write body before reducing it. Summaries retain only
supported frontend counters, connections, backend latency and scrape health.
They contain no arbitrary labels, request paths, credentials or private keys.
Each summary is at most 64 KiB. A node has one row with current and previous
samples; updates are atomic and strictly ordered by collection time. Duplicates
and late retries cannot replace newer data.

The live summary is published before the historical write. The agent still
receives success only after historical storage acknowledges the batch. Live
observations can therefore remain available during a historical-storage outage
without claiming that the same observation has reached history.

Live reads are bounded to twelve selected nodes, scoped by organization and
cluster, and retain permission, session and revocation checks on every stream
iteration. Counter rates use the latest healthy interval, handle resets and omit
rates until two samples exist. Collection gaps longer than fifteen seconds do
not produce invented zeros. HAProxy's reported rolling latency averages remain
unchanged.

During upgrades or an ingest rollback, nodes without fresh summaries use the
existing history-backed feed and its freshness checks. No agent protocol change
is required. The schema addition is compatible with older binaries; backup and
restore include the small latest-value table with the existing metadata dump.

This uses the independent Go database. It adds no Converge database traffic,
external cache service, approval flow or historical sample retention in SQL.

## Sources

- [Pinned searchable-row buffering](https://github.com/VictoriaMetrics/VictoriaMetrics/blob/v1.154.1/lib/storage/raw_row.go)
- [Pinned disk-flush behavior](https://github.com/VictoriaMetrics/VictoriaMetrics/blob/v1.154.1/lib/storage/partition.go)
- [VictoriaMetrics query latency and forced flush guidance](https://docs.victoriametrics.com/victoriametrics/keyconcepts/#query-latency)

---

HAProxy® is a registered trademark of HAProxy Technologies SAS. Ecraela Fleet
is not affiliated with or endorsed by HAProxy Technologies.
