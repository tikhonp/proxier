# Server redeploy

**Actors**: Admin; the redeploy job (queue `provisioning`); the server.

Because Proxier renders every file itself, it can change a server after provisioning by rendering again and applying only the difference. This covers **redeploy** (same version), **upgrade** (another version, alone or rolled out across many servers), **parameter changes**, and the lighter stack operations (**restart stack**, **update images**, **reboot**, **container logs**). Rotation is its own flow ([credential rotation](./credential-rotation.md)) but reuses this one.

## Steps — redeploy, upgrade, parameter change

1. The admin chooses an action on an **active** server's page:
   - **Redeploy**: the current version.
   - **Upgrade**: pick a newer (or older) version. The form asks for parameters the target version adds that have no default, and shows ones it removes.
   - **Edit parameters**: the current version with changed values.
2. Proxier renders the target for this server and compares it with the files of the last successful deployment. It shows the **plan**:
   - the files that will be added, changed and removed (a diff per file, secrets masked);
   - the generated values that will be created;
   - the endpoints that will change (a changed connection URI is flagged: "links will get new URIs on their next refresh");
   - the steps that will run.
3. Nothing differs → "Nothing to change", with **Force redeploy**, which uploads everything and runs the redeploy steps anyway.
4. **Apply** queues the job. The page follows its steps and log.
5. Job steps:
   1. Connect over SSH. A pinned host key is required.
   2. Upload the changed files and delete the removed ones (`upload-files`).
   3. Run the version's `redeploy` steps (in the seed template: `compose up`).
   4. Store the new endpoints, then run the self-check, then run the proxy test through every endpoint.
   5. Record the deployment, the server's version and parameters. → `server.redeployed{kind, from_version, to_version, files_changed}`
6. A failure stops the job. The server stays **active**, at whatever state the remote side reached, and the deployment is recorded as failed. Health checks report the actual effect. → `server.redeploy_failed{kind, step, error}` (notifies)
7. **Roll back** on a failed deployment opens a redeploy of the previous version with the previous parameters, through the same plan screen.

## Steps — rolling upgrade

1. The server list → select servers (or "all on older versions") → **Upgrade to default version**.
2. Proxier shows, per server, the version change and the number of changed files. Servers needing new parameters without defaults are listed and need values first.
3. **Start rollout** runs the upgrades **one server at a time**, in name order. The next server starts only after the previous one's proxy test passed.
4. The first failure stops the rollout. The servers after it are untouched, and the rollout shows done / failed / not started per server.

## Steps — lighter operations

| Operation | Job steps | Records |
|---|---|---|
| Restart stack | `docker compose restart` in `dir`, then a self-check | `server.redeployed{kind: restart}` |
| Update images | `docker compose pull`, `up -d`, self-check, proxy test | `server.redeployed{kind: images, changed_images}` |
| Reboot | `systemctl reboot`, wait for SSH (timeout 5 min), self-check, proxy test | `server.redeployed{kind: reboot}` |
| Container logs | `docker compose logs --tail 200` per service. Read-only; the output goes into the job log, which is visible only on the job page | nothing |

## Rules

- Only one mutating job runs per server at a time (resource key `server:<id>`). A redeploy waits for a running rotation, and the reverse.
- The plan is computed again when the job starts. If something changed between preview and apply (another deployment finished), the job applies the new difference and logs that the plan was recomputed.
- `upload-files` only removes files that an earlier deployment of this server created. Runtime data in `dir` (certificates, volumes) is never touched.
- Changing the template's default version never changes a server. Servers show "update available" until upgraded.
- Generated values a new version declares are created at upgrade. Values a version no longer declares are kept, so a rollback finds them.
- An endpoint key the target version no longer has disappears from the server's endpoints, and subscriptions stop serving it. The plan shows this as a warning before **Apply**.
- A redeploy never asks for the root password: Proxier's key is the only way in.

## Edge cases (each is a test)

- Redeploy with nothing changed → "Nothing to change", no job unless **Force redeploy**.
- Upgrade v1 → v2 where v2 changes only `site/index.html` → the plan shows one file. The job uploads it and runs `compose up`. Connection URIs unchanged.
- Upgrade where v2 adds parameter `log_level` without a default → the form asks for it before the plan is shown.
- Upgrade where v2 adds generated value `stats_token` → created at upgrade and listed in the plan.
- Upgrade where v2 removes `site/extra.css` that v1 deployed → the plan lists it as removed, and the job deletes it. `certbot/conf/` is untouched.
- Upgrade where v2 drops endpoint `main` → plan warning, and after apply the subscriptions no longer serve it.
- The server is unreachable → the job fails at connect, and the server's files are unchanged.
- `compose up` fails (bad image) → `server.redeploy_failed`. The page offers **Roll back**, and health reflects the broken stack on its next round.
- Edit parameters during a running health check round → the round finishes or is skipped. The redeploy runs after any other mutating job on the server.
- A rolling upgrade of five servers where the third fails its proxy test → two upgraded, one failed, two not started. The rollout stops and reports it.
- Update images where no image changed → the job succeeds and records `changed_images: []`.
- Reboot where SSH doesn't come back within 5 min → the job fails, and health checks report the server.
- A host key that changed since the last connection → the job fails with "host key changed" before uploading anything ([ADR 0007](../../adr/0007-agentless-ssh-with-pinned-host-keys.md)).
