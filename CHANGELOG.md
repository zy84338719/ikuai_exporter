# Changelog

## Unreleased

### Changed
- **v4-only**: dropped the v3 username/password (`/Action/login`) code path.
  The exporter now speaks a single mode — v4 REST over HTTPS with a personal
  Bearer token (`-token`). `-username`/`-password` are rejected.
- Migrated onto `ikuai-api` v1.0.1+ (v4-only SDK); the heavy `imroc/req` +
  `quic-go` transitive dependencies are gone (the SDK is now stdlib-only).
- `metrics.NewCollector` signature changed to `NewCollector(ns, router,
  client)`; every series carries a `router` label so one Prometheus job can
  distinguish many targets.
- Collapsed the old v3 session collector and v4 collector into a single v4
  collector built on `service.APIClient.Monitoring()`; responses are decoded
  from `json.RawMessage` into local structs (the SDK ships no typed models).

### Added
- `ikuai_connections_total` and `ikuai_connections{protocol}` metrics from
  `/monitoring/connections`.
- `-router-label` flag (defaults to the `-router` host).
- Health probes: `/live` (process up) and `/ready` (real router probe, 503
  when unreachable) for kubelet; `/healthz` kept as a `/live` alias.
- k8s readinessProbe now uses `/ready`.
- `metrics/collector_test.go` — first test coverage (JSON parsing, router-down
  degradation, helpers).

### Removed
- All v3 username/password deployment remnants (k8s secret/deployment,
  docker-compose).
