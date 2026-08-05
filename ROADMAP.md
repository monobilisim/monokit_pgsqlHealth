# pgsqlHealth Roadmap

PostgreSQL health plugin. Feature parity with monokit1 `pgsqlHealth/`.

## Migration

- [X] Convert to standalone submodule (own repo, module `github.com/monobilisim/monokit_pgsqlHealth`, template justfile/Containerfile/test harness, dropped `//go:build pgsqlHealth` tags)
- [ ] Containerfile: install postgresql for tests
- [ ] Podman integration tests

## Features

- [X] Up check (connection, alarm on failure/restore)
- [X] Process/activity check
- [X] Uptime monitoring
- [X] Active connection check vs percent of max_connections
- [X] Version check (moved to osHealth vlib)
- [X] Running query count check
  - [ ] Long-running query alerts (limits.long-query-time)
- [ ] WAL-G verify (scheduled; alarms + Redmine issues per check type)
- [ ] Patroni cluster monitoring
  - [ ] Service reachability alarm, cluster/node role status
  - [ ] Role-change detection against persisted previous state
  - [ ] Leader-switch hook (configured command executed on leader change)
  - [ ] Cluster-size Redmine issue with patronictl table
- [ ] Consul checks (service, ports 8500/8600, catalog services, member health)
- [ ] HAProxy checks (service, bind ports parsed from haproxy.cfg and probed)
- [ ] Run-as-postgres user handling
- [X] PMM agent check
