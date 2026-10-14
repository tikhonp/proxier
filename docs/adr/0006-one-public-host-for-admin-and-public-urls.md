# The admin UI and the public URLs share one public host, with password sign-in

The admin UI and the token URLs (`/s/`, `/r/`, `/f/`) are served on one public host, `proxier.tikhonnnnn.com`, through sh-main's nginx and the existing SSH tunnel. The admin UI is reachable from anywhere with a password; an authenticator app code and passkeys are opt-in ([ADR 0017](./0017-passkeys-sign-in-alone.md)). The admin chose reach and simplicity over tailnet-only access. The risk is reduced by lockout per IP, a notification on every sign-in from a new IP, short sessions and CSRF protection. The fixed route prefixes let a later gateway change move the admin to a private host, or the public URLs to another name, without code changes.

## Considered options

- **Admin on the tailnet only**: much smaller attack surface, but every admin device must be on the tailnet.
- **A separate public subdomain for link URLs**: lets a blocked or leaked link host be swapped without touching the admin. It remains possible later, though moving the public base URL changes every link URL.

## Consequences

- A stolen admin password gives control of every server, unless the authenticator app is on. Passkeys don't remove the risk: the password still signs in.
- Changing `PROXIER_BASE_URL` changes every link URL: the old name must keep serving until apps have refreshed from the new URLs.
