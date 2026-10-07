# Proxy tests and config validation use embedded xray-core; Proxier never gets the Docker socket

The proxy test must connect through an endpoint exactly as a real client does, discovery must browse through a chosen server, and template validation must reject xray configs that xray itself would refuse. xray-core is Go, so Proxier embeds it as a library for all three. The other validators (compose, nginx, shell) are pure Go too, so the image stays distroless with no `nginx` or `bash` in it: compose-go for compose, `mvdan.cc/sh` for shell (the syntax `bash -n` checks), and nginx-go-crossplane's parser for nginx, run on the rendered file with the context it will have in the container (decided 2026-10-07; this replaces the first plan of bundling `nginx -t` and `bash -n`). Validation was first imagined as throwaway containers on blackberry. That would need the Docker socket mounted into Proxier, which is root on the home server, in a service that sits on the public internet.

## Consequences

- The binary is larger because of xray-core, and xray updates come with Proxier releases.
- The nginx validation is a parse, not a run. It catches syntax errors, unknown directives, directives in the wrong context and wrong argument counts. It does not catch what only a running nginx finds (a missing certificate file, an upstream that does not resolve, a duplicate `listen`). The smoke test after provisioning remains the real check.
- The shell validation is a bash syntax check; it does not run ShellCheck.
