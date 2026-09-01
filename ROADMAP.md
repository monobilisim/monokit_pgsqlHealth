# pgsqlHealth Roadmap

PostgreSQL health plugin. Feature parity with monokit1 `pgsqlHealth/`.

## Migration

- [X] Convert to standalone submodule (own repo, module `github.com/monobilisim/monokit_pgsqlHealth`, template justfile/Containerfile/test harness, dropped `//go:build pgsqlHealth` tags)

## Features

- [X] Up check (connection, alarm on failure/restore)
  - [X] Credentials modes: `manual` (host/port/user/password/dbname) and `string` (connection string)
- [X] Process count check vs process-limit
- [X] Active query count check vs active-query-limit
- [X] Connection check vs connection-limit-percent of max_connections
- [X] Long-running query alerts (long-query.duration)
- [X] Uptime monitoring
- [X] Version check (moved to osHealth vlib)
- [X] WAL-G verify (wal-g.verify-hour scheduled; integrity + timeline alarms and Redmine issues; newest-backup age alarm; run-as-user support)
- [X] Patroni cluster monitoring (patroni.enabled)
  - [X] Config / REST API / service reachability alarms (TLS support from patroni.yml)
  - [X] Role-change detection against state persisted in the PatroniClusterMember table
  - [X] Leader-switch hook (patroni.leader-switch-hook, runs when this node becomes leader)
  - [X] Node state alarms (aggregated table of unhealthy members)
  - [X] Cluster-size Redmine issue with member table
- [X] Consul checks (consul.enabled: service, HTTP + DNS ports, unexpected catalog services, member health)
- [X] HAProxy checks (haproxy.enabled: service, bind ports parsed from haproxy.cfg and probed)
- [X] Run-as-postgres user handling (wal-g.run-as-user; DB access itself uses credentials, no user switch needed)
- [X] PMM agent check
- [ ] Docker-hosted PostgreSQL detection follow-ups (docker_check.go only logs today)
- [ ] Fallback connection-count query for PostgreSQL < 9.6 (no backend_type column)
- [ ] Health summary box output, compact and full (depends on the lib renderer)
- [ ] Health data POST to the server API (depends on base client/server API)

## Tests

Each suite has its own Containerfile and its own parallel CI job
(`just test-<suite>` locally, matrix job in the GitHub workflow; `just test`
runs them one by one). Artifacts land in logs/ as monokit2-<suite>.db/.log.

- [X] postgres suite — Containerfile.postgres, PostgreSQL 12, 15 and 18 from PGDG
  - [X] TestConnectPSQL / TestConnectPSQLStringMode / TestConnectPSQLUnknownMode
  - [X] TestCheckActivity (process / active-query / connection-percent thresholds down + up)
  - [X] TestCheckLongRunningQueries (pg_sleep query triggers and clears the alarm)
  - [X] TestGetUptime
- [X] walg suite — Containerfile.walg, stubbed wal-g binary
  - [X] TestCheckWalG (failing verify + stale backup down, then OK + fresh backup up, Redmine transitions)
  - [X] TestParseWalVerifyStatus
- [X] patroni suite — Containerfile.patroni, stubbed Patroni REST API
  - [X] TestCheckPatroni (role change + leader hook + node states + cluster-size issue, then recovery)
  - [X] TestCheckPatroniUnreachableAPI
- [X] consul suite — Containerfile.consul, stubbed Consul HTTP API
  - [X] TestCheckConsul (rogue catalog service + failing member, then recovery)
  - [X] TestCheckConsulClosedPorts
- [X] haproxy suite — Containerfile.haproxy, real haproxy driven via systemd
  - [X] TestParseHAProxyBindPorts
  - [X] TestCheckHAProxy (running config, stopped service, restart recovery)
