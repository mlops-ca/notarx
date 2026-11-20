# Observability

Grafana dashboards for operators (ADR 0023 section 11). Import a file from `grafana/` and pick
the Prometheus data source that scrapes both the notarx `/metrics` endpoint and a
[postgres_exporter](https://github.com/prometheus-community/postgres_exporter) instance
with the `stat_statements` collector enabled.

| Dashboard | Shows |
| --- | --- |
| `grafana/notarx-database.json` | top statements by total time, calls and rows per call (`pg_stat_statements`), dead tuples, partition count, default-partition rows, retention lag and deletions, audit flush p99 |

`pg_stat_statements` must be enabled for the first three panels: see
[PostgreSQL capabilities](../../docs/postgresql-capabilities.md#pg_stat_statements).

`go test ./deploy/observability` (part of `make test`) validates every dashboard: well-formed
JSON, unique panel ids, a query on every panel and no metric other than those the notarx binary
exports (found in its source) or the short list of postgres_exporter series in
`dashboards_test.go`.
