# pgsqlHealth Roadmap

PostgreSQL health plugin. Feature parity with monokit1 `pgsqlHealth/`.

## Migration

- [X] Convert to standalone submodule (own repo, module `github.com/monobilisim/monokit_pgsqlHealth`, template justfile/Containerfile/test harness, dropped `//go:build pgsqlHealth` tags)

## Features

- [X] Up check (`ping` alarm on connection failure/restore, 10s connect timeout so a frozen server can't hang the plugin)
  - [X] Credentials modes: `manual` (host/port/user/password/dbname, values quoted so passwords with spaces/quotes work) and `string` (connection string)
- [X] Process count check vs process-limit
- [X] Active query count check vs active-query-limit
  - [X] Client backends only: walsenders, autovacuum and parallel workers on PostgreSQL 10+ are not queries
  - [X] Alarm lists the active queries (PID, duration, user, database, client, query), longest first, top 10
- [X] Connection check vs connection-limit-percent of max_connections
  - [X] PostgreSQL 9.6 and older (no backend_type column): every pg_stat_activity row counts as a connection
  - [X] Over the limit, every connection is written to `<log dir>/pgsql-stat_activity-<Weekday>.log` (one file per weekday, a week kept; monokit1 used /var/log/monodb) and the alarm names the file
- [X] Long-running query alerts (long-query.duration)
  - [X] Client backends only, so a streaming replica's walsender (active since replication started) no longer alarms forever on the primary
  - [X] Alarm lists the long-running queries, same table as the active-query alarm
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
- [X] Docker-hosted PostgreSQL (containers found by image repository; narrowed to the ones publishing the configured port)
  - [X] dockerContainer alarm: restarting, paused, dead or unhealthy container; recovery only once one is running
  - [X] Container states listed in the `ping` down alarm (stopped containers are reported there)
  - [X] WAL-G runs inside the container (`docker exec --user <run-as-user>`) when the host has no wal-g
- [ ] Health summary box output, compact and full (depends on the lib renderer)
- [ ] Health data POST to the server API (depends on base client/server API)

## Tests

Each suite has its own Containerfile and its own parallel CI job
(`just test-<suite>` locally, matrix job in the GitHub workflow; `just test`
runs them one by one). Artifacts land in logs/ as monokit2-<suite>.db/.log.

- [X] postgres suite — Containerfile.postgres, PostgreSQL 9.6 (PGDG archive), 12, 15 and 18 from PGDG
  - [X] TestConnectPSQL / TestConnectPSQLStringMode / TestConnectPSQLUnknownMode
  - [X] TestConnectPSQLQuotedPassword (space, quote and backslash in a manual-mode password)
  - [X] TestConnectPSQLTimeout (server that accepts TCP and never answers)
  - [X] TestCheckUp (closed port down, then up on every version)
  - [X] TestCheckActivity (process / active-query / connection-percent thresholds down + up, query table in the alarm, snapshot file lists the connection)
  - [X] TestCheckLongRunningQueries (pg_sleep query triggers and clears the alarm, alarm lists the sleeper)
  - [X] TestCheckLongRunningQueriesIgnoresWalsender (real pg_receivewal stream on 12/15/18 raises no alarm)
  - [X] TestActivityFormatting (ordering, row cap, pipe escaping, NULL columns, rune-safe truncation)
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
- [X] docker suite — Containerfile.docker, stubbed docker CLI
  - [X] TestDockerIsPostgresImage
  - [X] TestDockerFindContainers (image filter, port narrowing in manual and string mode)
  - [X] TestDockerCheckContainers (unhealthy, leftover next to a running replacement, crash loop, full stop, paused, gone)
  - [X] TestDockerCheckUp (stopped container listed in the ping alarm)
  - [X] TestDockerWalG (no wal-g in container, ambiguous containers, verify via docker exec down and up)
