# Passkeys sign in alone; the authenticator app guards the password; both are opt-in

The admin can add passkeys and turn on an authenticator app on Settings → Security; with neither, sign-in is the password alone, as before. A passkey signs in by itself, with no username, password or code. The device must check who holds it (user verification), so the passkey is already two factors: something the admin has, unlocked by something the admin is or knows. The authenticator app adds a 6-digit code to a password sign-in only. Password sign-in stays possible whatever else is set up. The way back in without the phone or the passkeys is `proxier manage reset-2fa` in the container shell, the same trust root as `reset-password`.

## Considered options

- **A passkey as a second step after the password**: no gain over a passkey alone (the passkey already proves two factors) and slower every time.
- **A "passkeys only" switch** that turns password sign-in off: the strongest setup, but one more state to lock oneself out with, for one admin who can already keep the password long and the app on. Not now.
- **Recovery codes** for the authenticator app: another secret to store and keep safe, when the container shell already restores access and is needed for `reset-password` anyway.

## Consequences

- A stolen password alone still signs in unless the app is on. Passkeys make phishing the admin harder, not the password stronger.
- Every change to the ways in needs the current password and notifies, so a stolen session can't quietly add a way in.
- The relying party is `PROXIER_BASE_URL`. Moving Proxier to another host name makes every passkey useless there: sign in with the password and add them again.
- Failed codes and passkeys count toward the same per-IP lockout as failed passwords; a right password before its code doesn't reset the count.
