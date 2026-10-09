-- +goose Up

CREATE TABLE rscripts_scripts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    slug            TEXT NOT NULL UNIQUE,
    description     TEXT NOT NULL DEFAULT '',
    current_version INTEGER,                                    -- NULL until the first publish
    archived        INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0,1)),
    created_at      TEXT NOT NULL,
    FOREIGN KEY (id, current_version) REFERENCES rscripts_versions(script_id, number) DEFERRABLE INITIALLY DEFERRED
) STRICT;

-- Immutable, numbered without gaps. The body is stored byte for byte and
-- parsed on demand; only the warnings confirmed at publish are kept.
CREATE TABLE rscripts_versions (
    script_id    INTEGER NOT NULL REFERENCES rscripts_scripts(id) ON DELETE CASCADE,
    number       INTEGER NOT NULL CHECK (number >= 1),
    body         TEXT NOT NULL,
    sha256       TEXT NOT NULL,                                 -- hex of the body
    warnings     TEXT NOT NULL DEFAULT '[]',                    -- JSON params.Finding list confirmed at publish
    notes        TEXT NOT NULL DEFAULT '',
    published_at TEXT NOT NULL,
    published_by TEXT NOT NULL,
    PRIMARY KEY (script_id, number)
) STRICT;

CREATE TABLE rscripts_drafts (
    script_id  INTEGER PRIMARY KEY REFERENCES rscripts_scripts(id) ON DELETE CASCADE,
    body       TEXT NOT NULL,
    based_on   INTEGER,                                         -- the version it was made from; NULL for a new script
    revision   INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

-- A version filled in for one router (4b). The file itself is never stored:
-- it is params.Fill(the version's body, the values) when it is downloaded or
-- fetched. A script with generations can't be deleted (the version reference
-- refuses the cascade).
CREATE TABLE rscripts_generations (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    script_id         INTEGER NOT NULL,
    version           INTEGER NOT NULL,
    router_name       TEXT NOT NULL,                            -- as entered; names the file and, when registered, the router
    file_name         TEXT NOT NULL,                            -- "<slug>-<router>-v<n>.rsc"
    vals              TEXT NOT NULL DEFAULT '{}',               -- JSON {name: value} of every parameter that isn't secret
    secrets           BLOB,                                     -- vault "generation:<id>:secrets": JSON {name: value} of the @secret ones and the link's URL; NULL when none
    changed           TEXT NOT NULL DEFAULT '',                 -- parameters whose value differs from the default, comma-joined, in script order
    link_id           INTEGER,                                  -- subscriptions' link, by value; NULL when none was used
    link_name         TEXT NOT NULL DEFAULT '',
    link_created      INTEGER NOT NULL DEFAULT 0 CHECK (link_created IN (0,1)),
    router_id         INTEGER,                                  -- routing's router, by value; NULL when not registered
    router_registered INTEGER NOT NULL DEFAULT 0 CHECK (router_registered IN (0,1)),
    key_fingerprint   TEXT NOT NULL DEFAULT '',                 -- Proxier's key a @fill proxier-ssh-key parameter got
    created_at        TEXT NOT NULL,
    created_by        TEXT NOT NULL,
    FOREIGN KEY (script_id, version) REFERENCES rscripts_versions(script_id, number),
    CHECK (link_created = 0 OR link_id IS NOT NULL),
    CHECK (router_registered = 0 OR router_id IS NOT NULL)
) STRICT;
CREATE INDEX rscripts_generations_script ON rscripts_generations(script_id, id);

-- A generation's single-use, short-lived URL (4c). The token is erased the
-- moment the URL ends.
CREATE TABLE rscripts_fetch_urls (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    generation_id INTEGER NOT NULL REFERENCES rscripts_generations(id),
    token         BLOB,                                         -- vault "fetch_url:<id>:token"; NULL once the URL ended
    token_lookup  BLOB UNIQUE,                                  -- vault.Lookup(token); NULL with the token
    state         TEXT NOT NULL CHECK (state IN ('waiting','used','expired','replaced')),
    created_at    TEXT NOT NULL,
    created_by    TEXT NOT NULL,
    expires_at    TEXT NOT NULL,
    ended_at      TEXT,                                         -- used, replaced, or found expired
    ip            TEXT NOT NULL DEFAULT '',                     -- of the fetch that used it
    user_agent    TEXT NOT NULL DEFAULT '',                     -- at most 256 characters
    CHECK ((token IS NULL) = (token_lookup IS NULL)),
    CHECK ((state = 'waiting') = (token IS NOT NULL)),
    CHECK ((state = 'waiting') = (ended_at IS NULL))
) STRICT;
-- At most one live fetch URL per generation.
CREATE UNIQUE INDEX rscripts_fetch_urls_waiting ON rscripts_fetch_urls(generation_id) WHERE state = 'waiting';
CREATE INDEX rscripts_fetch_urls_generation ON rscripts_fetch_urls(generation_id, id);

-- +goose Down
DROP TABLE rscripts_fetch_urls;
DROP TABLE rscripts_generations;
DROP TABLE rscripts_drafts;
DROP TABLE rscripts_versions;
DROP TABLE rscripts_scripts;
