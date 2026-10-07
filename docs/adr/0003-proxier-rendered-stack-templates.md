# Server templates are rendered by Proxier, not scripts run on the server

A template version is a manifest plus files. Proxier generates the secrets (UUIDs, secret paths), renders every file with Go templates, uploads them over SSH, and runs declared steps built from a small set of step kinds. Because Proxier holds the exact configuration of every server, it can redeploy, upgrade with a diff, and rotate credentials later. Turning a VPS into a server becomes data, versioned and validated in the UI.

## Considered options

- **Run the existing bash scripts non-interactively and parse their output**: the fastest port. But Proxier would only know what a script printed, and every later change (rotation, new users, a new decoy site) would need yet another script.
- **Ansible playbooks**: idempotent and good for ongoing changes, but Python and Ansible in the image and a second language, while the stacks are just a few files and `docker compose`.

## Consequences

- `servers-templates/proxy/setup.sh` is converted once into the seed template. The repository can stay as the place to keep and review templates, imported by zip or git URL.
- Templates must render the xray client list from `.Clients`, so that per-link credentials, if they ever arrive, are a data change and not a template rewrite.
