# Vendored files

Nothing here is loaded from a CDN at run time (CSP: everything from 'self').

| File | What | Source | sha256 |
|---|---|---|---|
| `js/htmx.min.js` | htmx 2.0.11 | https://cdn.jsdelivr.net/npm/htmx.org@2.0.11/dist/htmx.min.js | d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717 |
| `fonts/plex-mono-{latin,cyrillic}-{400-normal,400-italic,600-normal,700-normal}.woff2` | IBM Plex Mono (the Russian UI needs the Cyrillic subset) | https://cdn.jsdelivr.net/npm/@fontsource/ibm-plex-mono@5/files/ | |
| `fonts/OFL.txt` | IBM Plex license (SIL OFL 1.1) | https://github.com/IBM/plex | |

`css/tokens.css` is the app's copy of the design tokens, minus the Google Fonts import. From Phase 0b on it is the source of record, not `docs/ui/design/tokens.css`.
