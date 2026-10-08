### Manifest

The seed template, converted from `servers-templates/proxy` ([VLESS XHTTP integration](../integrations/vless-xhttp.md)), shows every part:

```yaml
name: VLESS XHTTP behind nginx
description: VLESS over XHTTP, hidden behind nginx with a Let's Encrypt certificate and a decoy site.

requires:
  os: [debian-12, debian-13, ubuntu-22.04, ubuntu-24.04]
  arch: [amd64, arm64]
dir: /opt/proxier/vless-xhttp        # where the stack lives on the server
ports: [80/tcp, 443/tcp]             # opened in the firewall, besides SSH

parameters:                          # asked when building a server
  - key: letsencrypt_email
    label: Let's Encrypt email
    type: email
    required: false
    sample: admin@example.com        # used by validation

generated:                           # created once per server, reused on every redeploy
  - { key: client_uuid, kind: uuid, rotate: true }
  - { key: xhttp_path, kind: hex, length: 16, prefix: /, rotate: true }
  - { key: container_postfix, kind: hex, length: 4 }

files:                               # rendered with Go templates, uploaded into dir
  - { path: compose.yaml, validate: compose }
  - { path: xray-config.json, validate: xray }
  - { path: nginx/default.conf.template, validate: nginx }
  - { path: site/index.html }
  - { path: .env, mode: "0600" }
  - { path: issue-cert.sh, mode: "0755", validate: shell }

steps:
  install:                           # provisioning
    - base-bootstrap
    - upload-files
    - run: ./issue-cert.sh
      timeout: 5m
    - compose-up: { pull: true }
    - wait-http: { url: "https://{{ .Server.ProxyHostname }}/", resolve: 127.0.0.1, status: 200 }
  redeploy:                          # redeploy, upgrade, rotation
    - upload-files
    - compose-up
  uninstall:                         # retirement with "remove the stack"
    - compose-down: { volumes: true }

endpoints:                           # what clients connect to
  - key: main                        # stable across versions; subscriptions rely on it
    type: vless-xhttp-tls
    host: "{{ .Server.ProxyHostname }}"
    port: 443
    credential: "{{ .Gen.client_uuid }}"
    params:
      path: "{{ .Gen.xhttp_path }}"
      sni: "{{ .Server.ProxyHostname }}"
      mode: stream-up
      fp: chrome
      alpn: h2

checks:                              # the self-check
  - compose-running
  - http-local: { url: "https://{{ .Server.ProxyHostname }}/", status: 200 }
  - cert-expiry: { file: "certbot/conf/live/{{ .Server.ProxyHostname }}/fullchain.pem" }
  - disk-free

proxy_test:                          # optional; overrides Settings → Servers → proxy test URL
  url: "https://speed.cloudflare.com/__down?bytes=262144"
```

**Parameters** have a `key`, `label`, `type` (`string`, `email`, `int`, `bool`, `choice` with `options`, `text`), optional `required`, `default`, `sample`, `help`, and `secret` (stored encrypted, masked in the UI).

**Generated values** have a `key` and a `kind`: `uuid` (v4), `hex` (`length` characters), `base64` (`bytes`), or `password` (`length`, letters and digits). Each can have a `prefix`. `rotate: true` marks the values that rotation replaces. They are created on first use for a server and kept for the server's life. Upgrading to a version that declares a new one creates it. A value no longer declared is kept, unused, so a rollback still works.

**Files** have a `path`, an optional `mode` (default `0644`) and an optional `validate` (`xray`, `compose`, `nginx`, `json`, `yaml`, `shell`). A path is relative and `/`-separated, with no `..`, no leading `/` and no `./`, at most 255 bytes, and only `A-Z a-z 0-9 . _ - /` (paths end up in shell commands and SFTP paths). A file in the template that `files` does not list is an error, except `manifest.yaml`. A file is at most 1 MiB, and a version at most 10 MiB, counted on the files as written (before rendering).

**Steps** are lists for `install`, `redeploy` and `uninstall`. `install` must start with `base-bootstrap`, then `upload-files`, and `redeploy` must start with `upload-files`; neither kind may appear anywhere else, nor in `uninstall`. The provisioning and deploy jobs run those as steps of their own (with DNS and the generated values in between during provisioning), so their place is fixed. A `run` step whose command starts with `./` must name a file listed in `files` (`./issue-cert.sh` needs `issue-cert.sh`). Built-in step kinds:

| Step | What it does |
|---|---|
| `base-bootstrap` | Works as the `proxier` deploy user that provisioning created (passwordless sudo, Proxier's and your personal SSH keys). Turns off password login and root login over SSH. Writes `.hushlogin`, installs the apt prerequisites `curl ca-certificates openssl ufw` (waiting for apt's lock, which a fresh VPS often holds), and installs Docker (get.docker.com) if missing. Turns on the firewall (ufw) with the SSH port plus `ports`. Docker's published ports bypass ufw; the manifest's ports are opened in ufw anyway so the rule set says what is meant, and nothing else listens. Never locks Proxier out: each SSH change is verified with a fresh key login and rolled back if that fails. |
| `upload-files` | Renders every file and uploads each one to a temporary name before renaming it into place, so no file is half-written. Deletes files that an earlier deployment of this server put there and this version no longer has. Never touches anything else in `dir` (runtime data such as `certbot/`). |
| `run` | Runs a command or a bundled script in `dir`, with a timeout (default 10 min). Exit code 0 is success. Output goes to the job log. |
| `compose-up` | `docker compose up -d --remove-orphans`, with `pull: true` pulling images first. |
| `compose-down` | `docker compose down`, with `volumes: true` adding `-v`. |
| `wait-http` | Polls a URL until it answers with the expected status (default timeout 2 min). With `resolve`, the request runs on the server through `curl --resolve`. |

**Endpoints** declare what clients connect to: a stable `key`, an endpoint `type`, `host`, `port`, `credential` and type-specific `params`. They are rendered on every deployment and stored, so subscriptions always serve current values.

**Checks** are the self-check, run over SSH every 5 minutes:

| Check | Passes when |
|---|---|
| `compose-running` | Every compose service is running, and healthy if it declares a healthcheck. |
| `http-local` | The URL, requested on the server itself (`curl --resolve`), returns the status. |
| `cert-expiry` | The certificate exists. Fewer than 14 days left raises `server.cert_expiring` but doesn't fail the check. An expired certificate fails it. |
| `disk-free` | More than 10 % of the root filesystem is free (below that, `server.disk_low`). Under 2 % fails the check. |
| `run` | A custom command exits 0. |

### Rendering

Files, step arguments, endpoint fields and check arguments are Go templates. Missing keys are errors. The context is:

| Name | Example |
|---|---|
| `.Server.Name`, `.Server.Number` | `nl-1`, `1` |
| `.Server.Location.Code`, `.Name`, `.Country` | `nl`, `Netherlands`, `NL` |
| `.Server.IP`, `.Server.SSHPort` | `203.0.113.10`, `22` |
| `.Server.ManagementHostname`, `.Server.ProxyHostname` | `nl-1.hosts.tikhonnnnn.com` |
| `.Params.<key>` | parameters |
| `.Gen.<key>` | generated values |
| `.Clients` | the client identities the stack must accept: a list of `{ID, Name}`. In the first version it holds exactly one entry, the endpoint credential named `shared`. |
| `.Template.Slug`, `.Template.Version` | `vless-xhttp`, `3` |

Functions: `json`, `quote`, `default`, `lower`, `upper`, `trim`, `urlquery`, `b64enc`, `sha256`, `join`, `indent`.
