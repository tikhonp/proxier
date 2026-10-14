# Sign-in

**Actors**: Member; Proxier; `proxier manage` (the container shell).

Every account is a member, and each member signs in on their own: their own password, sessions, authenticator app and passkeys. The command line makes the first member; the others are added on Settings → Members and choose their password at a setup link ([members](./members.md)). Sign-in is a password on a public host, so the rules focus on lockout, short sessions and telling about every sign-in from a new IP. Two ways in are opt-in: an **authenticator app** whose code a password sign-in then also needs, and **passkeys**, each of which signs in alone ([ADR 0017](../../adr/0017-passkeys-sign-in-alone.md)). With neither, sign-in is the password alone.

## Steps: the first member and the command line

1. `docker exec -it proxier /bin/proxier manage create-admin` makes the first member (member 1), as Owner. It asks for a username and a password, twice. The password must be at least 12 characters. → `member.added{access: owner}` (actor `cli`)
2. Running it again when any member exists refuses: "A member already exists; use reset-password <username> to get back in."
3. `proxier manage reset-password <username>` sets a new password for that member and ends their sessions. → `auth.password_changed{via: cli}`. A removed member's username names nobody here.
4. `proxier manage reset-2fa <username>` turns that member's authenticator app off, removes their passkeys and ends their sessions; the password stays. It asks nothing and needs no terminal. It records its event only when it removed or ended something. → `auth.second_factor_reset{via: cli, totp, passkeys, sessions}` (notifies)
5. A username nobody has: "There is no member "<username>"." (or, with no members at all, "use create-admin"). Nothing is asked and nothing changes.

## Steps: signing in

1. Every URL that needs a session redirects to `/login?next=<path>` without one.
2. The member enters username and password. The username is matched ignoring case. If the IP is locked, the form says "Too many attempts. Try again in N min" and checks nothing.
3. Wrong username or password: the same message, "Wrong username or password", after the same delay. → `auth.sign_in_failed{ip, username_tried}`
4. The 5th failure from one IP within 15 minutes locks that IP for 15 minutes. → `auth.locked{ip, failures}` (notifies)
5. Correct: a new session is created with a fresh cookie, and the browser goes to `next` (a local path only, otherwise the dashboard). → `auth.signed_in{ip, user_agent, new_ip, method: password}`. A sign-in from an IP that has no successful sign-in of that member in the last 30 days has `new_ip: true` and notifies.

## Steps: the code step

With the authenticator app on, step 5 changes:

1. A correct password creates no session and records no successful attempt. The page asks for the 6-digit code from the app. The sign-in waits there 5 minutes, for at most 5 codes, and only for the same IP.
2. A wrong code (or one already used): "Wrong code", after the same delay as a wrong password. It is a failed attempt, counted with the wrong passwords for the lockout. → `auth.sign_in_failed{ip, stage: code}`
3. A right code: the session is created as in step 5. → `auth.signed_in{…, method: password+code}`
4. Out of time or tries, from another IP, or the app turned off meanwhile: "The sign-in ran out. Enter the password again." with the password form.

## Steps: passkeys

1. **Adding** (Settings → Security → Passkeys): a name (1 to 64 characters) and the current password, then **Add passkey**. The browser asks the device, the password manager or the security key to make one; it must check who holds it (user verification). The passkey is stored with its name. → `auth.passkey_added{name}` (notifies)
2. Once a passkey exists, the sign-in page offers **Sign in with a passkey**, and the browser offers passkeys in the username field's autofill. Before that it has no passkey part at all.
3. **Signing in**: the browser shows the passkeys it holds for this site; the member picks one and confirms with Face ID, Touch ID, a PIN or the key. No username, no password, no code: the passkey's user handle names the member. → `auth.signed_in{…, method: passkey}`
4. A refused passkey (unknown, removed, from another site, without user verification, a signature counter that went back) is a failed attempt: "That passkey wasn't accepted." → `auth.sign_in_failed{ip, stage: passkey}`
5. **Removing**: **Remove…** on its row asks for the current password. → `auth.passkey_removed{name}` (notifies)

## Steps: the authenticator app

1. **Set up…** (Settings → Security → Authenticator app) asks for the current password, then shows a QR code and the key as text. Nothing is stored yet.
2. The member scans it and types the code the app shows. A right code turns the app on (the secret is stored in the vault). → `auth.totp_enabled` (notifies). A wrong code shows the same QR code again with "Wrong code"; after 15 minutes the setup has run out and starts again.
3. **Turn off** asks for the current password. → `auth.totp_disabled` (notifies)

## Steps: sessions

1. Each request with a session updates its last-seen time (at most once a minute). A session ends after 7 days without requests or 30 days after creation, whichever comes first.
2. **Sign out**, the last cell of the header on every page, ends the current session with one click, with no confirmation, and goes to the sign-in page.
3. Settings → Security lists the member's own sessions (created, last seen, IP, browser, "this one"). **Sign out** on a row ends that one. **Sign out everywhere** ends all of the member's sessions, including the current one. → `auth.signed_out_everywhere{sessions}`
4. **Change password** asks for the current password and the new one twice. It ends the member's other sessions. → `auth.password_changed{via: ui}` (notifies)

## Rules

- An expired session redirects to `/login?ended=idle` (or `absolute`) and the page says "Signed out after 7 days idle. You'll go back to <page>."
- A successful sign-in resets the failure count of that IP.

- Members are added and removed on Settings → Members ([members](./members.md)), never renamed. A removed member, or one who hasn't chosen a password at their setup link, signs in by no way at all: the same "Wrong username or password" as an unknown username.
- Usernames are unique ignoring case among the members who aren't removed, 1 to 64 characters.
- Everything a member does in the UI records the actor `member:<id>`. Rows from before members say `admin`, which is member 1; Activity and the Jobs page name both by username.
- Cookies are `HttpOnly`, `Secure`, `SameSite=Lax`, and scoped to the Proxier host. Public routes (`/s/`, `/r/`, `/f/`) never read or set them.
- Every request that changes something carries a CSRF token tied to the session.
- The client IP comes from `X-Real-IP` only when the request arrives from a trusted proxy. Otherwise it is the socket address. Lockout and "new IP" use that IP.
- Lockouts and the attempt history survive restarts.
- The sign-in page and its messages follow the browser's language until signed in, then the member's setting. A browser that asks for neither English nor Russian gets the default language (Settings → General).
- The lockout counts failed passwords, codes and passkeys together, per IP whatever the username, and refuses a locked IP before checking any of them. Only a created session counts as a success. An attempt keeps the member it named, if any.
- Adding or removing a passkey and turning the app on or off need the current password, so a stolen session can't add a way in or remove one.
- A code works once: the step of the last used code is stored, and only a newer one is accepted (the previous, current and next 30 s step, for clock drift).
- The relying party of passkeys is `PROXIER_BASE_URL`: a passkey made on another host or origin isn't accepted.
- Each member's authenticator app secret is sealed in the vault with the AAD `member:<id>:totp`. Member 1's keeps `admin:totp`, the AAD it had before members: migrations are SQL without the master key, so they can't re-seal it.
- The code step and a passkey prompt live in memory: a restart only makes the member start that sign-in again.

## Edge cases (each is a test)

- `create-admin` with an 11-character password → refused, asked again.
- `create-admin` when a member exists → refused, nothing changes.
- `reset-password` or `reset-2fa` with a username nobody has → refused, nothing asked, nothing changes.
- A username typed in another case (`ADMIN`) → signs in as that member.
- Two members: sessions, **Sign out**, **Sign out everywhere** and `reset-password` of one leave the other's alone, and an IP one member used is still new to the other.
- A passkey signs in as the member who added it; another member neither lists nor removes it.
- An authenticator app turned on before members → member 1's codes still work after the upgrade.
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
- **Sign out** from the header → the session ends; the next page redirects to `/login`. Without the CSRF token → `403`, and the session goes on.
- Without passkeys or the app → the password alone signs in (`method: password`); the sign-in page loads no passkey script.
- App on, right password → no session and no successful attempt; the code step.
- App on, right code → signed in (`method: password+code`). The same code again on a new sign-in → "Wrong code".
- Codes of the previous and the next 30 s step → accepted; two steps away → refused.
- 2 wrong passwords, then the right password and 3 wrong codes → locked; the right code is refused with the lockout message.
- 5 wrong codes → the pending sign-in is gone. After 5 minutes, or from another IP → "The sign-in ran out".
- App turned off while a sign-in waits for its code → "The sign-in ran out"; the password alone works.
- Setup: a wrong password → refused; a wrong code → not on; a setup from another session, tampered, or older than 15 minutes → refused. The secret is stored only sealed.
- Turning the app off with a wrong password → refused; turning it off twice records one event.
- A passkey signs in with the app on → no code asked.
- A passkey without user verification → not added, and doesn't sign in.
- A passkey answer from another origin or for another RP ID → refused.
- An unknown or removed passkey → refused, a failed attempt.
- A passkey's signature counter going back → refused.
- A passkey prompt used twice, unknown, or older than 5 minutes → "ran out"; an add prompt can't sign in, a sign-in prompt can't add, and an add prompt can't be finished by another session.
- The same passkey added twice → refused.
- Adding with a wrong password or a name of 0 or 65 characters → refused. Removing with a wrong password → refused.
- A locked IP with a valid passkey → the lockout message; another IP signs in.
- A passkey sign-in with `next=//evil.example` → the dashboard.
- Each new Security POST without the CSRF token → `403`.
- `reset-2fa` with nothing to remove and no session → no event. With the app, a passkey and a session → all gone, the session ended (`second_factor_reset`), and the password alone signs in.
