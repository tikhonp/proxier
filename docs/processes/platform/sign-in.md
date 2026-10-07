# Sign-in

**Actors**: Admin; Proxier; `proxier manage` (the container shell).

There is one admin. Sign-in is a password on a public host with no second factor, so the rules focus on lockout, short sessions and telling the admin about every sign-in from a new IP.

## Steps — creating the admin

1. `docker exec -it proxier /bin/proxier manage create-admin` asks for a username and a password, twice. The password must be at least 12 characters. → `admin.created`
2. Running it again when an admin exists refuses: "An admin already exists; use reset-password."
3. `proxier manage reset-password` sets a new password and ends every session. → `auth.password_changed{via: cli}`

## Steps — signing in

1. Every admin URL without a valid session redirects to `/login?next=<path>`.
2. The admin enters username and password. If the IP is locked, the form says "Too many attempts. Try again in N min" and checks nothing.
3. Wrong username or password: the same message, "Wrong username or password", after the same delay. → `auth.sign_in_failed{ip, username_tried}`
4. The 5th failure from one IP within 15 minutes locks that IP for 15 minutes. → `auth.locked{ip, failures}` (notifies)
5. Correct: a new session is created with a fresh cookie, and the browser goes to `next` (a local path only, otherwise the dashboard). → `auth.signed_in{ip, user_agent, new_ip}`. A sign-in from an IP that has no successful sign-in in the last 30 days has `new_ip: true` and notifies.

## Steps — sessions

1. Each request with a session updates its last-seen time (at most once a minute). A session ends after 7 days without requests or 30 days after creation, whichever comes first.
2. **Sign out** ends the current session.
3. Settings → Security lists the sessions (created, last seen, IP, browser, "this one"). **Sign out** on a row ends that one. **Sign out everywhere** ends all, including the current one. → `auth.signed_out_everywhere{sessions}`
4. **Change password** asks for the current password and the new one twice. It ends every other session. → `auth.password_changed{via: ui}` (notifies)

## Rules

- An expired session redirects to `/login?ended=idle` (or `absolute`) and the page says "Signed out after 7 days idle. You'll go back to <page>."
- A successful sign-in resets the failure count of that IP.

- The admin can't be created, deleted or renamed from the UI.
- Cookies are `HttpOnly`, `Secure`, `SameSite=Lax`, and scoped to the admin host. Public routes (`/s/`, `/r/`, `/f/`) never read or set them.
- Every request that changes something carries a CSRF token tied to the session.
- The client IP comes from `X-Real-IP` only when the request arrives from a trusted proxy. Otherwise it is the socket address. Lockout and "new IP" use that IP.
- Lockouts and the attempt history survive restarts.
- The sign-in page and its messages follow the browser's language until signed in, then the admin's setting.
- A second factor, when added, sits between step 5's password check and session creation. Nothing else changes.

## Edge cases (each is a test)

- `create-admin` with an 11-character password → refused, asked again.
- `create-admin` when an admin exists → refused, nothing changes.
- Wrong password → generic message, `auth.sign_in_failed`, no session.
- Unknown username → the same message and the same timing as a wrong password.
- 4 failures, then success → signed in. The failure count doesn't lock.
- 5th failure within 15 min → `auth.locked`, one notification. A 6th attempt with the correct password → still refused with the lockout message.
- Lockout expires after 15 min → the correct password works.
- Failures from two IPs → each counted separately.
- `X-Real-IP` sent by a direct (untrusted) client → ignored; the socket address is used.
- Sign-in from an IP signed in successfully 10 days ago → `new_ip: false`, no notification. From one last seen 40 days ago → notification.
- `next=https://evil.example/` or `next=//evil.example` → redirected to the dashboard.
- A session idle for 7 days and 1 minute → redirected to `/login`.
- A session active every day for 30 days → ends at day 30 anyway.
- Password change on the laptop → the phone's session ends at once. The laptop's stays.
- `reset-password` from the shell during a lockout → works, and all sessions end. The lockout still applies to that IP until it expires.
- A POST without a CSRF token → `403`, nothing changes.
