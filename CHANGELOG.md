# Changelog

## v1.0.1 — 2026-08-01

Field-mapping fixes verified against a live iKuai 4.0.303 router.

### Fixed
- `/monitoring/system`: read memory from the `memory` key (was `mem`); the
  payload nests under `sysinfo`, already unwrapped.
- `/monitoring/clients-online`: peel the inner `{"data":[...]}` wrapper (was
  unmarshalling the wrapper object as a bare array, yielding zero devices).
- `/monitoring/connections` is a timeseries, not a summary — dropped.
  Connection counters (`ikuai_connections_total`, `ikuai_connections{protocol}`)
  now come from `sysinfo.stream` (a real-time point value).

Verified live: CPU (9 cores), memory, connections (tcp/udp/icmp), 11 online
devices, wan/lan interfaces with traffic.

## v1.0.0 — 2026-08-01

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
