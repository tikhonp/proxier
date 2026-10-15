# The UI and the public URLs share one public host, with password sign-in

The UI members sign in to and the token URLs (`/s/`, `/r/`, `/f/`) are served on one public host, `proxier.tikhonnnnn.com`, through sh-main's nginx and the existing SSH tunnel. The UI is reachable from anywhere with a password; an authenticator app code and passkeys are opt-in ([ADR 0017](./0017-passkeys-sign-in-alone.md)). Reach and simplicity were chosen over tailnet-only access. The risk is reduced by lockout per IP, a notification on every sign-in from a new IP, short sessions and CSRF protection. The fixed route prefixes let a later gateway change move the UI to a private host, or the public URLs to another name, without code changes.

When this was decided, one person signed in. Several members now do, each with their own password and a level in each area ([ADR 0018](./0018-several-members-with-per-area-access.md)); the decision holds for all of them.

## Considered options

- **The UI on the tailnet only**: much smaller attack surface, but every member's device must be on the tailnet.
- **A separate public subdomain for link URLs**: lets a blocked or leaked link host be swapped without touching the UI. It remains possible later, though moving the public base URL changes every link URL.

## Consequences

- A stolen password gives what its member's grant reaches, unless their authenticator app is on. With `servers: manage` that is every server. Passkeys don't remove the risk: the password still signs in.
- Changing `PROXIER_BASE_URL` changes every link URL: the old name must keep serving until apps have refreshed from the new URLs.
