# Jobs

**Actors**: Admin; modules (enqueue jobs from actions and event reactions); Scheduler; worker pools.

All long or remote work is a job: provisioning, redeploys, check rounds, router syncs, refreshes, discovery, notifications, backups. Jobs are stored, so the admin can see what ran, what failed and why, retry it, or cancel it, and a restart doesn't lose work. The machinery is described in [architecture](../../architecture.md#jobs). This document is the behaviour.

## Steps — lifecycle

1. A module enqueues a job: type, queue, payload, resource key, optional coalescing key, optional delay, created by (admin / schedule / event).
   - If the coalescing key matches a **queued** job, the new request merges into it: the payload is merged and the later of the two run-after times is kept. No new job is created.
   - If it matches a **running** job, one follow-up is queued (or merged into the existing follow-up).
2. When its run-after time has passed, a worker of its queue with free capacity claims it, provided no other job with the same resource key is running. The job becomes `running` with a lease, and its attempt count goes up.
3. The job runs its steps in order. Each step is marked running, then succeeded or failed, with timings. Log lines stream to the job page as they are written, redacted.
4. All steps succeeded → `succeeded`.
5. A step failed:
   - attempts are left and the error is retryable → `queued` again, after the backoff for that type, from the failed step;
   - otherwise → `failed`, and the module records its own failure event (or `job.failed` when it has none).
6. **Cancel** on a queued job → `cancelled` at once. On a running job, the cancel is requested. The handler stops at the next step boundary (or sooner where a step supports it) → `cancelled`. Steps already done are not undone unless the job type defines a cleanup.
7. **Retry** on a failed or cancelled job creates a new job ("retry of #123") that starts at the step that didn't finish. It runs even if the original had no attempts left.

## Steps — restart

1. On startup, every `running` job whose lease has expired is marked `interrupted` in its log.
2. Interrupted jobs are queued again and resume from the first step that didn't succeed. Every step is written to be safe to run twice.
3. A job whose type says it can't resume (none in the first version) becomes `failed` with "interrupted by restart".

## Rules

- At most one running job per resource key (`server:12`, `router:3`). Health checks don't queue behind mutating jobs: a check round for a server whose key is busy is skipped and recorded as skipped.
- A job never holds a database transaction across a remote call. It reads, acts remotely, then writes the result.
- Secrets a job handles are registered with its logger. Lines are scrubbed before they are stored, so no log, job page or download contains them.
- A job's payload stores its secret parts encrypted. The root password of a provisioning job is removed from the payload as soon as key login works.
- Logs are capped at 10,000 lines per job. Beyond that the middle is dropped, and a line says how many were dropped.
- Scheduled jobs never overlap themselves: a schedule whose previous job is still queued or running skips its turn and records the skip.
- Retention: jobs and their logs for 30 days, failed ones for 90 days.

Default retry policies:

| Job type | Attempts | Backoff |
|---|---|---|
| provision, redeploy, upgrade, rotate, retire | 1 (the admin retries) | — |
| check rounds | 1 (the next round comes anyway) | — |
| router sync | 4 | 5 min, 15 min, 1 h |
| upstream refresh, catalog refresh | 3 | 10 min, 30 min |
| Telegram delivery | 3 | 10 s, 1 min, 5 min |
| backup | 2 | 15 min |

## Edge cases (each is a test)

- Two router-sync requests for router 3 within 30 s → one job runs.
- A sync request while router 3's sync is running → exactly one follow-up queued. A third request merges into that follow-up.
- A redeploy for server 12 while its health check round is due → the check round is skipped, not queued.
- Two provisioning jobs for different servers → both run (concurrency 2). A third waits.
- A step fails with a retryable error, and attempts are left → queued again after the backoff, starting at that step. The log shows both attempts.
- Cancel a queued job → `cancelled`, never claimed.
- Cancel a running provisioning job during "Install Docker" → it stops after that step. State `cancelled`, the server `failed`, and the log says who cancelled.
- The container restarts while a router sync is in its push step → after start the job is `interrupted`, then resumes. The service block it re-runs is idempotent, and the router ends correct.
- Retry a failed job → a new job linked to the old one, starting at the failed step.
- A log line containing the root password, a generated UUID or a link token → stored with `•••`.
- A job writing 15,000 lines → 10,000 kept, with a marker line.
- A daily schedule whose previous run is still going → skip recorded, no second job.
- A job failed 31 days ago → still listed (failed jobs are kept 90 days). One that succeeded 31 days ago → gone.
