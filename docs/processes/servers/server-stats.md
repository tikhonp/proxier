# Server stats

**Actors**: the self-check job; Admin.

Stats show what each server is doing: load, memory, disk and traffic. No agent is installed. The numbers are read in the same SSH session as the self-check, every 5 minutes ([ADR 0007](../../adr/0007-agentless-ssh-with-pinned-host-keys.md)).

## Steps — collection

1. During each self-check, Proxier reads in one command batch:
   - `/proc/loadavg` (1-minute load)
   - `/proc/stat` (CPU busy % since the previous sample)
   - `/proc/meminfo` (used and total memory, without caches)
   - `df` of `/` (used and total)
   - the counters of the interface carrying the default route from `/proc/net/dev` (rx/tx bytes)
   - `/proc/uptime`
   - `docker ps` states of the stack's containers
2. CPU % and traffic are computed as differences from the previous sample. The first sample after an activation or a reboot stores counters only.
3. A sample is stored with the server and time. An hourly job rolls raw samples older than 7 days into hourly averages (traffic: sums), kept 90 days.

## Steps — display

1. **Server list**: CPU, memory and disk as small bars, and traffic today (rx + tx).
2. **Stats tab**: charts of CPU %, load, memory, disk, and traffic (rx and tx, per 5 min / per hour) for 24 h, 7 d and 30 d. Current values, uptime, traffic this month, and container states (running, restarting, exited, with restart counts).
3. **Dashboard**: the servers with the highest disk use and the most traffic today.

## Rules

- Stats are read-only facts. They never change health on their own. Health uses its own checks (`disk-free` among them).
- A sample whose counters went down (reboot, counter reset) stores no traffic delta, and the next sample starts from the new counters.
- Missing samples (server unreachable, checks paused) are gaps in the charts, never zeros.
- Times are UTC in storage and shown in the display time zone. "Traffic today" and "this month" use the display time zone's day and month.

## Edge cases (each is a test)

- First sample after activation → load, memory, disk and uptime stored; no CPU % or traffic delta.
- Second sample → CPU % and traffic delta computed from the first.
- Uptime decreases between samples (reboot) → no traffic delta for that sample, and the next one is normal.
- Server unreachable for 30 min → six missing samples, shown as a gap.
- Raw samples 8 days old → rolled up into hourly averages and traffic sums, then deleted.
- Traffic "today" at 00:30 Moscow time → counts only samples since 00:00 Moscow time.
- A server with two network interfaces → only the default-route interface is counted.
