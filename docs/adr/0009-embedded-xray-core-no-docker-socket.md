# Proxy tests and config validation use embedded xray-core; Proxier never gets the Docker socket

The proxy test must connect through an endpoint exactly as a real client does, discovery must browse through a chosen server, and template validation must reject xray configs that xray itself would refuse. xray-core is Go, so Proxier embeds it as a library for all three. The other validators (compose, nginx, shell) run inside Proxier's own container: compose-go, and the bundled `nginx -t` and `bash -n` on a sandboxed copy. Validation was first imagined as throwaway containers on blackberry. That would need the Docker socket mounted into Proxier, which is root on the home server, in a service that sits on the public internet.

## Consequences

- The binary is larger because of xray-core, and xray updates come with Proxier releases.
- The nginx validation is a sandbox. Certificate paths and upstream hostnames are substituted so `nginx -t` can run, and what it catches is syntax and directive errors, not runtime behaviour. The smoke test after provisioning remains the real check.
