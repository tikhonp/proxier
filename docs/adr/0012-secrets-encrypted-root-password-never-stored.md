# Secrets are encrypted under one master key; a new VPS's root password is never kept

Every secret in the database (generated values, link and config tokens, deployed files that contain credentials, integration tokens, Proxier's SSH private key) is encrypted with AES-256-GCM under a master key from the environment. Tokens are looked up through an HMAC, never by their plain value. The root password of a new VPS lives only, encrypted, in its provisioning job, and is erased as soon as login with Proxier's key works. After that, no Proxier database, log or backup can reveal it.

## Consequences

- Losing the master key loses every secret. It is kept in the secrets submodule and in Vaultwarden. Backups are useless without it, which is also what makes them safe to copy around.
- A provisioning job that fails before key login keeps the password, encrypted, until it is retried, cancelled or the server is retired. A retry after that point asks for it again.
