# VLESS over XHTTP (the seed template and endpoint type)

The first endpoint type, `vless-xhttp-tls`, and the seed template "VLESS XHTTP behind nginx" come from `servers-templates/proxy`. The stack is unchanged. What changes is who runs it: Proxier renders the files and runs the steps instead of an interactive `setup.sh`.

## The stack

```
client --TLS:443--> nginx --/<xhttp-path>/...--> xray (VLESS over XHTTP, h2c via grpc_pass)
                          --anything else-------> decoy one-page site
```

| Service | Role |
|---|---|
| `nginx` (`nginx:stable-alpine`) | Terminates TLS with the Let's Encrypt certificate. Rejects handshakes without the right SNI (`ssl_reject_handshake` on the default server), so the domain can't be learned from the IP. Forwards only `<xhttp-path>/` to xray over h2c (`grpc_pass`), serves the decoy site for everything else, and reloads every 6 h to pick up renewed certificates. |
| `xray` (`ghcr.io/xtls/xray-core`) | VLESS inbound on `10001` with `network: xhttp`, `security: none`, the secret path, `mode: auto`, and padding `100-1000` bytes. No published ports. Sniffing on, `freedom` outbound. |
| `certbot` (`certbot/certbot`) | Renews the certificate every 12 h via webroot. The first certificate is issued by the install step in standalone mode, because nginx can't start without one. |

## Template contents

| File | From `servers-templates/proxy` | Change |
|---|---|---|
| `compose.yaml` | `compose.yaml` | none |
| `nginx/default.conf.template` | same | none (`${SERVER_DOMAIN}`, `${VLESS_XHTTP_PATH}` still come from `.env` through the nginx image's envsubst) |
| `site/index.html` | same | none |
| `xray-config.json` | `xray-config.json` with `VLESS_CLIENT_UUID` / `VLESS_XHTTP_PATH` placeholders replaced by `sed` | Go template: `clients` rendered from `.Clients` ([servers module](../modules/servers.md#future-per-link-credentials)), `path` from `.Gen.xhttp_path` |
| `.env` | written line by line by `setup.sh` | rendered: `CONTAINER_POSTFIX={{ .Gen.container_postfix }}`, `SERVER_DOMAIN={{ .Server.ProxyHostname }}`, `VLESS_XHTTP_PATH={{ .Gen.xhttp_path }}` |
| `issue-cert.sh` | `issue_certificate()` in `setup.sh` | a standalone script that exits early when the certificate exists (so retries are safe) and adds `-m <email> --no-eff-email` only when the parameter is set |

What `setup.sh` did interactively and Proxier now does itself:

| `setup.sh` | Proxier |
|---|---|
| `bootstrap-system.sh` (Docker, docker group, SSH password hardening) | `base-bootstrap` step, plus key installation and the firewall |
| Asks for the domain and checks that it resolves | The hostname comes from the server name. DNS is created through Cloudflare and waited for. |
| Asks for the Let's Encrypt email | Template parameter `letsencrypt_email` |
| `uuidgen`, `openssl rand -hex 8`, `openssl rand -hex 2` | Generated values `client_uuid`, `xhttp_path` (16 hex characters with a leading `/`), `container_postfix` (4 hex characters) |
| Asks for the link's tag name | The endpoint's display name (`🇳🇱 Netherlands 1`) |
| `credentials.txt` | The server page and subscription outputs |
| `newgrp docker` subshell | Not needed: Proxier runs Docker through `sudo -n` as its `proxier` deploy user |

## Connection URI

Built exactly as `setup.sh` printed it:

```
vless://{uuid}@{host}:443?encryption=none&security=tls&sni={host}&fp=chrome&host={host}&alpn=h2&type=xhttp&path=%2F{path-without-leading-slash}&mode=stream-up#{display-name}
```

| Part | Value |
|---|---|
| `uuid` | the endpoint credential (`.Gen.client_uuid`) |
| `host` | the proxy hostname |
| `path` | the secret path. It is hex, so only the leading `/` needs encoding. |
| `mode=stream-up` | One streaming POST through nginx's `grpc_pass`. The server accepts any mode, so a client may switch to `packet-up` (e.g. behind a CDN). |
| `fp=chrome`, `alpn=h2` | as in the original |
| `#display-name` | percent-encoded UTF-8, e.g. `%F0%9F%87%B3%F0%9F%87%B1%20Netherlands%201` |

## Checks for this template

- **Self-check**: `compose-running` (nginx, xray, certbot running), `http-local` (the decoy site returns `200` when asked for the proxy hostname on the server itself), `cert-expiry` on `certbot/conf/live/<host>/fullchain.pem`, `disk-free`.
- **Proxy test**: three steps ([health](../processes/servers/server-health.md#checks)). xray's own errors are opaque, so two direct probes make the class of a network failure certain before xray is involved:
  1. a plain TCP connection to `host:443` (5 s): no answer in time → `tcp-timeout`, a refusal → `tcp-refused`;
  2. a TLS handshake with the endpoint's SNI, ALPN and **fingerprint** (the same ClientHello a real client sends, so a network that treats a Go handshake differently from Chrome's does not mislead the probe), checking the certificate (5 s) → `tls-failed`;
  3. the test object downloaded through the embedded xray client, which builds an outbound from the endpoint (XHTTP in the endpoint's mode, TLS with `fp` and `alpn`), with a 15 s overall timeout and a 5 s stall timer that restarts whenever data arrives: the stall timer fires → `stalled` (with the bytes received), the overall one → `timeout`, a non-2xx answer or any other failure of the request (a refused credential shows as a connection closed with no answer) → `http-error` (the error text says which).

  A failing probe ends the test: step 3 would only repeat it less clearly. Success records the connect and TLS times of the probes, the time to the first byte and the throughput of step 3, and the bytes received; a body under 64 KiB carries a "small object" warning, because throttling after ~20 KB cannot be seen on it.

## Ports and firewall

`ports: [80/tcp, 443/tcp]`. Port 80 serves ACME challenges and redirects to HTTPS. 443 is the endpoint. SSH stays on the server's SSH port.
