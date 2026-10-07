# One binary on SQLite, with durable job queues inside the process

Proxier is a single Go binary in one container. Its state is one SQLite file, and its background work (provisioning, checks, syncs, refreshes, notifications) runs as jobs stored in that file and executed by worker pools inside the same process. Proxier serves one admin and a handful of servers and routers, so a separate database server, a message broker or a worker fleet would add moving parts to deploy and back up without solving a problem it has. SQLite (one writer, WAL) fits a single process, a backup is copying one file, and stored jobs give the restart-safety, retries, cancellation and live logs that the long tasks need.

## Considered options

- **Separate worker binary sharing the SQLite file** (the alcs pattern): independent restarts, but two writers on one SQLite file, and nothing gained at this scale.
- **Remote workers or agents pulling jobs over an API**: needed only if work must run elsewhere. If probe agents arrive, they will be an external-checker driver, not a second job system.
- **Postgres + a queue library**: more infrastructure for no current need.

## Consequences

- Every job step must be safe to run twice, because an interrupted job resumes from its first unfinished step after a restart.
- Splitting the work across processes later would mean moving to a database that tolerates several writers. Keep module code free of in-process assumptions beyond the job API.
