# Router sync hang on Telepat (2026-10-12)

The first sync of a real router never finished. The router imported everything, but job #3837 stays `running` with no new log line, so the router shows `syncing` and its preview spins. This report holds what was observed in production (read-only; nothing was changed, cancelled or restarted) and where the code suggests the hang is. The cause is **not confirmed**: the hypotheses below come from reading the code, not from a reproduction.

Times are Moscow time (the router's clock and the job page); the container log is UTC, three hours behind.

## Setup

- Proxier `main-4c64eb6` on blackbox (100.64.0.9), container up since 2026-10-11 20:48 UTC, healthy.
- Router 2, **Telepat**: MikroTik hEX S, RouterOS 7.24.5 (stable), 192.168.2.126. It follows the list Main (29 services, 1 332 domains).
- Path: Proxier's tsnet node → jump host raspberry-telepat (192.168.2.120, on the tailnet as 100.64.0.12) → router port 22. The router sees Proxier's commands as `ssh-cmd:proxier@192.168.2.120`.
- Proxier's router user `proxier`, group `proxier`.

## Timeline

| Time (MSK) | What |
|---|---|
| ~15:38 | The router boots (uptime 1 h 11 min at 16:49). |
| 16:05 | Router 2 edited (`POST /routing/routers/2/edit`). |
| 16:13, 16:16 | Connection tests run and confirmed (`/routing/routers/tests/1..4`). |
| 16:16:30 | Job #3837 starts: Router sync · Telepat, queue `routers`, resource `router:2`, started by job 3836, payload `trigger: initial`, attempt 1 of 4. |
| 16:16:31 | `Connected to Telepat: RouterOS 7.24.5 (stable) on hEX S.` |
| 16:16:32 | `Plan: 29 new, 0 updated, 0 removed, 0 unchanged, 0 recorded.` |
| 16:16:32 | `File 1/1 proxier-sync-3837-1.rsc: update chesscom (+2), … update tunneled-domains (+40).` The router shows the file written at 16:16:32 (370.3 KiB). **Last log line of the job.** |
| 16:16:3x–16:18:31 | The router runs the import: its log shows `static dns entry added` / `address list entry added by ssh-cmd:proxier@192.168.2.120` lines up to 16:18:31 (last one: `zmk.dev`, tag `tunneled-domains`). |
| ~16:46:32 | When `routeros.ImportTimeout` (30 min) should have ended the import command. Nothing was logged. |
| 16:22–16:24, 16:40–16:46 | The router page polls `GET /routing/routers/2/preview` about once a second (385 requests in the container log), all 200; the preview shows "Reading the router…". |
| 17:00 | Job still `running`, still 5 log lines, step "Read, plan, push, verify" with no duration. Proxier has **no active session** on the router (`/user active print` lists only the admin's own). |

## What the router holds

The import completed. Counts of entries by comment match the plan exactly (DNS static and address list alike): meta 481, youtube 177, ikea 95, cloudflare 76, instagram 74, google-gemini 46, discord 43, tunneled-domains 40, tiktok 37, x 28, jetbrains 27, openai 22, telegram 21, claude.ai 20, wikimedia 18, kinopub 17, brawlstars.com 17, ubiquiti 13, ookla-speedtest 13, linkedin 12, …

`proxier-sync-3837-1.rsc` is **still on the router**: `push.go` removes a file only after its import command returns, and the job never got there. Verify never ran, so Proxier recorded no tag as applied.

The router's memory log holds 1 000 lines and the import filled it, so `proxier`'s login and logout lines are gone; when its session ended is unknown. The router's own DoH failed until 16:20 (`SSL: internal error`, "max concurrent queries"); that is the router's DNS, unrelated to Proxier, but noted because it overlaps the import.

## What Proxier's logs hold

Nothing about the job: stdout carries only request lines (and tsnet start-up). No error, no panic, no 5xx in 24 h; the only non-INFO lines are favicon 404s. Job state, steps and log are in the database only (read through the job page).

## Where it hangs (hypotheses, unverified)

The job's last line comes right before `cl.Run(ctx, routeros.CmdImport(f.Name), sshx.RunOptions{Timeout: routeros.ImportTimeout, Log: log})` in `internal/modules/routing/routers/push.go:42`. The upload (`cl.Put`) succeeded: the file is on the router. So the goroutine is almost certainly inside `sshx.Client.Run` (`internal/platform/sshx/client.go:314`) for the `/import`.

1. **`Run`'s timeout and cancel paths wait without a bound.** When the timer (or `ctx`) fires, `Run` sends `SIGKILL`, closes the session, then does `<-done`, where `done` is fed by `sess.Wait()` (`client.go:351-355`). x/crypto's `Session.Wait` returns only when the peer closes the channel (or the connection dies). If the router had already forgotten the channel (it shows no `proxier` session), it never answers our close, and `<-done` blocks forever. That fits both facts: the import finished and the 30-minute timeout passed with nothing logged. It also means **Cancel would hang the same way** (the `ctx.Done()` branch uses the same `<-done`), and a graceful shutdown would wait for the job until Docker's 30 s grace kills the process.
2. **Why `Wait` didn't return when the import ended.** RouterOS ran the whole file by 16:18:31 but the client never saw an exit status / EOF / channel close. Candidates: RouterOS ending the `ssh-cmd` channel without `exit-status` + `close` after a long `/import` (or after its 10 min `inactivity-timeout`; `proxier`'s `inactivity-policy` is `none`), or the close being lost between the jump host's `direct-tcpip` channel and the router. The keepalive (`client.go:268`, every 30 s, 3 misses close the client) would not help if the connection itself stays alive and only this one channel is orphaned.
3. **The preview spinning** is probably a consequence: it reads the router and likely waits on the job holding `router:2`. Not checked in the code.

## How to reproduce (suggested)

`sshxtest.Server` can probably stand in for the router: a command that starts, then has its channel dropped on the server side without `exit-status`/`close`, and that ignores the client's close. Expect `Run` with a short `Timeout` to return `ErrTimeout`; today it should hang at `<-done`. A second case: the same through a jump host, for hypothesis 2's path. Running the real `/import` of a large file against the hEX S (Telepat) through raspberry-telepat would show whether RouterOS sends `exit-status`, e.g. with `ssh -v proxier@… '/import file-name=… verbose=no'`.

## What a fix needs to cover

- `Run` must return within its timeout (plus a short grace) and on cancel even when the peer never acknowledges the close: bound the wait for `done`, then close the whole client if needed, keeping the guarantee that nothing writes to the job log after `Run` returns.
- After the fix, a hung import must end as a sync failure for that attempt (file left or removed per `push.go`'s rules, `landed` deciding what took effect), so the retry policy (5 min, 15 min, 1 h) runs.
- If hypothesis 2 holds, the import should end when RouterOS finishes even without `exit-status` (e.g. treat a channel close / EOF without status as the end and judge the result by `ScanImport` and verify).
- Each case above gets a test (CLAUDE.md: every edge case gets a test).

## State left in production (untouched)

- Job #3837 `running` on `router:2`; the router `syncing`.
- `proxier-sync-3837-1.rsc` on the router.
- Everything planned is already on the router; the next successful sync should plan 0 new and record the tags.
- To unstick it once fixed: restarting the container marks the job `interrupted` and resumes it (the resume removes the job's files); Cancel may not work until hypothesis 1 is fixed.
