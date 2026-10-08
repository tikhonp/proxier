-- +goose Up

-- A service: a named set of domains from one source. Its tag is its identity
-- on routers (comment=) and in Shadowrocket (# tag).
CREATE TABLE routing_services (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    tag              TEXT NOT NULL UNIQUE,
    source           TEXT NOT NULL CHECK (source IN ('v2fly','iplist','url','custom')),
    selector         TEXT NOT NULL DEFAULT '',      -- the stored spelling; '' for custom
    name             TEXT NOT NULL DEFAULT '',      -- custom: its display name; '' upstream (the tag names it)
    description      TEXT NOT NULL DEFAULT '',
    origin           TEXT NOT NULL DEFAULT '' CHECK (origin IN ('','discovery','import','router')),
    last_checked_at  TEXT,                          -- the last refresh that got an answer (3c)
    last_error       TEXT NOT NULL DEFAULT '',      -- the last refresh's error; '' after a success (3c)
    failures         INTEGER NOT NULL DEFAULT 0,    -- consecutive failed refreshes (3c)
    failing_notified INTEGER NOT NULL DEFAULT 0 CHECK (failing_notified IN (0,1)), -- refresh_failing sent for this run (3c)
    created_at       TEXT NOT NULL,
    CHECK ((source = 'custom') = (selector = ''))
) STRICT;

-- What a service held at a moment. Exactly one accepted snapshot per service:
-- the one targets get. Names are text, sorted and "\n"-joined.
CREATE TABLE routing_snapshots (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id   INTEGER NOT NULL REFERENCES routing_services(id) ON DELETE CASCADE,
    status       TEXT NOT NULL CHECK (status IN ('accepted','superseded','rejected')),
    selector     TEXT NOT NULL DEFAULT '',          -- what it was fetched from; '' custom
    portal       TEXT NOT NULL DEFAULT '',          -- iplist: main, beta, russia
    kind         TEXT NOT NULL DEFAULT '',          -- iplist: site or group
    suffix       TEXT NOT NULL DEFAULT '',
    exact        TEXT NOT NULL DEFAULT '',          -- never a name that is also suffix
    skipped      TEXT NOT NULL DEFAULT '[]',        -- JSON [{"entry":…,"reason":…}]
    suffix_count INTEGER NOT NULL,
    exact_count  INTEGER NOT NULL,
    hash         TEXT NOT NULL,                     -- snapshot.Set.Hash()
    added        INTEGER NOT NULL DEFAULT 0,        -- against the accepted snapshot it was compared with
    removed      INTEGER NOT NULL DEFAULT 0,
    reason       TEXT NOT NULL DEFAULT '',          -- rejected (3c): 'empty' or 'shrink'; kept when accepted anyway
    lost_pct     INTEGER NOT NULL DEFAULT 0,        -- rejected for shrink: the percent lost
    forced_by    TEXT NOT NULL DEFAULT '',          -- accepted anyway (3c): the actor
    in_round     INTEGER NOT NULL DEFAULT 0 CHECK (in_round IN (0,1)), -- made by the daily round (3c)
    dismissed_at TEXT,                              -- rejected and dismissed (3c)
    fetched_at   TEXT NOT NULL,
    accepted_at  TEXT,                              -- when it became the accepted one
    CHECK (status <> 'rejected' OR reason <> ''),
    CHECK (dismissed_at IS NULL OR status = 'rejected'),
    CHECK ((status = 'rejected') = (accepted_at IS NULL))
) STRICT;
CREATE UNIQUE INDEX routing_snapshots_accepted ON routing_snapshots(service_id) WHERE status = 'accepted';
CREATE INDEX routing_snapshots_service ON routing_snapshots(service_id, id);

-- The editable domains of a custom service. Each save also writes a snapshot.
CREATE TABLE routing_custom_domains (
    service_id INTEGER NOT NULL REFERENCES routing_services(id) ON DELETE CASCADE,
    domain     TEXT NOT NULL,                       -- normalised, punycode
    exact      INTEGER NOT NULL CHECK (exact IN (0,1)),
    note       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (service_id, domain)
) STRICT;

CREATE TABLE routing_lists (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    is_default  INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
    created_at  TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX routing_lists_one_default ON routing_lists(is_default) WHERE is_default = 1;
INSERT INTO routing_lists (name, description, is_default, created_at)
    VALUES ('Main', '', 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

-- The services of a list, in order (3b). A service in a list can't be deleted.
CREATE TABLE routing_list_services (
    list_id    INTEGER NOT NULL REFERENCES routing_lists(id) ON DELETE CASCADE,
    service_id INTEGER NOT NULL REFERENCES routing_services(id),
    position   INTEGER NOT NULL,                    -- 1..n, dense, rewritten on every change
    added_at   TEXT NOT NULL,
    PRIMARY KEY (list_id, service_id)
) STRICT;
CREATE INDEX routing_list_services_service ON routing_list_services(service_id);

-- The catalog (3c). Each source writes its rows as a new generation and then
-- switches to it; only a source's current generation is read.
CREATE TABLE routing_catalog_sources (
    source        TEXT PRIMARY KEY CHECK (source IN ('v2fly','iplist:main','iplist:beta','iplist:russia')),
    generation    INTEGER NOT NULL DEFAULT 0,       -- in force; 0 = never refreshed
    refreshed_at  TEXT,                             -- when the generation in force was made
    revision      TEXT NOT NULL DEFAULT '',         -- v2fly: the commit
    entries       INTEGER NOT NULL DEFAULT 0,
    failures      INTEGER NOT NULL DEFAULT 0,       -- consecutive failed refreshes
    failing_since TEXT,
    last_error    TEXT NOT NULL DEFAULT '',
    notified      INTEGER NOT NULL DEFAULT 0 CHECK (notified IN (0,1)) -- catalog_refresh_failed sent for this run
) STRICT;
INSERT INTO routing_catalog_sources (source) VALUES ('v2fly'), ('iplist:main'), ('iplist:beta'), ('iplist:russia');

-- What search finds: v2fly lists, iplist sites and groups.
CREATE TABLE routing_catalog_entries (
    source     TEXT NOT NULL,
    generation INTEGER NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('list','site','group')),
    name       TEXT NOT NULL,                       -- the list, site or group: its tag
    grp        TEXT NOT NULL DEFAULT '',            -- a site's group
    sites      INTEGER NOT NULL DEFAULT 0,          -- a group's sites
    domains    INTEGER NOT NULL DEFAULT 0,          -- names, includes resolved
    PRIMARY KEY (source, generation, kind, name)
) STRICT;

-- The reverse index: the list or site that holds a name directly.
CREATE TABLE routing_catalog_domains (
    source     TEXT NOT NULL,
    generation INTEGER NOT NULL,
    name       TEXT NOT NULL,                       -- the v2fly list or the iplist site
    domain     TEXT NOT NULL,
    exact      INTEGER NOT NULL CHECK (exact IN (0,1)),
    attrs      TEXT NOT NULL DEFAULT ''             -- v2fly: the entry's attributes, "@ads @cn"
) STRICT;
CREATE INDEX routing_catalog_domains_domain ON routing_catalog_domains(domain);
CREATE INDEX routing_catalog_domains_generation ON routing_catalog_domains(source, generation);

-- v2fly includes, so a lookup can name the lists that hold a name through one.
CREATE TABLE routing_catalog_includes (
    generation INTEGER NOT NULL,
    list       TEXT NOT NULL,
    included   TEXT NOT NULL,
    filter     TEXT NOT NULL DEFAULT '',            -- "@ads", "@-!cn", '' for none
    PRIMARY KEY (generation, list, included, filter)
) STRICT;

-- A MikroTik router (3e).
CREATE TABLE routing_routers (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT NOT NULL UNIQUE,
    list_id            INTEGER NOT NULL REFERENCES routing_lists(id),
    state              TEXT NOT NULL CHECK (state IN ('awaiting','active','paused','removing')),
    host               TEXT NOT NULL,
    port               INTEGER NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
    ssh_user           TEXT NOT NULL,
    jump_host          TEXT NOT NULL DEFAULT '',
    jump_port          INTEGER NOT NULL DEFAULT 22 CHECK (jump_port BETWEEN 1 AND 65535),
    jump_user          TEXT NOT NULL DEFAULT '',
    tailnet            INTEGER NOT NULL DEFAULT 0 CHECK (tailnet IN (0,1)), -- the first hop goes through the tailnet node
    address_list       TEXT NOT NULL DEFAULT 'to_vpn_list',
    doh_forwarder      TEXT NOT NULL DEFAULT 'vpn-doh',
    version            TEXT NOT NULL DEFAULT '',
    board              TEXT NOT NULL DEFAULT '',
    connected_at       TEXT,                        -- the first successful connection
    last_seen_at       TEXT,
    last_sync_at       TEXT,
    last_sync_result   TEXT NOT NULL DEFAULT '' CHECK (last_sync_result IN ('','synced','failed')),
    last_sync_error    TEXT NOT NULL DEFAULT '',
    failures           INTEGER NOT NULL DEFAULT 0,  -- consecutive failed sync attempts
    failure_notified   INTEGER NOT NULL DEFAULT 0 CHECK (failure_notified IN (0,1)), -- a given-up sync notified since the last success (3e)
    drift              TEXT NOT NULL DEFAULT '',    -- tags that drifted at the last check, comma-joined (3f)
    drift_checked_at   TEXT,
    unmanaged_notified TEXT NOT NULL DEFAULT '',    -- unmanaged tags already notified, comma-joined (3f)
    untagged           INTEGER NOT NULL DEFAULT 0,  -- at the last read
    infra_pins         INTEGER NOT NULL DEFAULT 0,  -- at the last read
    read_at            TEXT,
    awaiting_until     TEXT,                        -- awaiting setup: probed until then (3f)
    created_by         TEXT NOT NULL,               -- admin; routerscripts (Phase 4)
    created_at         TEXT NOT NULL,
    CHECK ((jump_host = '') = (jump_user = '')),
    CHECK ((state = 'awaiting') = (awaiting_until IS NOT NULL))
) STRICT;

-- What Proxier last installed on a router, per tag (3e).
CREATE TABLE routing_router_tags (
    router_id  INTEGER NOT NULL REFERENCES routing_routers(id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    hash       TEXT NOT NULL,                       -- routeros.EntriesHash of the applied entries
    suffix     INTEGER NOT NULL,
    exact      INTEGER NOT NULL,
    applied_at TEXT NOT NULL,
    PRIMARY KEY (router_id, tag)
) STRICT;

-- Tags on a router that Proxier never installed (3f).
CREATE TABLE routing_router_unmanaged (
    router_id  INTEGER NOT NULL REFERENCES routing_routers(id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    entries    INTEGER NOT NULL,                    -- DNS entries with that comment
    names      TEXT NOT NULL DEFAULT '[]',          -- JSON [{"name":…,"exact":…}], at most 5 000
    first_seen TEXT NOT NULL,
    last_seen  TEXT NOT NULL,
    ignored_at TEXT,
    PRIMARY KEY (router_id, tag)
) STRICT;

-- Syncs, previews, drift checks and removals of a router (3e, 3f).
CREATE TABLE routing_syncs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    router_id   INTEGER NOT NULL REFERENCES routing_routers(id) ON DELETE CASCADE,
    job_id      INTEGER,                            -- by value
    kind        TEXT NOT NULL CHECK (kind IN ('sync','preview','drift','removal')),
    trigger     TEXT NOT NULL DEFAULT '',           -- sync: change, manual, initial, drift, resume, list, unmanaged
    state       TEXT NOT NULL CHECK (state IN ('running','done','failed','cancelled')),
    step        TEXT NOT NULL DEFAULT '',           -- failed: connect, read, push, verify
    hop         TEXT NOT NULL DEFAULT '' CHECK (hop IN ('','tailnet','jump','router')), -- failed at connect: the hop (3e)
    error       TEXT NOT NULL DEFAULT '',
    read_at     TEXT,
    plan        TEXT NOT NULL DEFAULT '[]',         -- JSON: routers.TagPlan rows, without their names
    script      TEXT NOT NULL DEFAULT '',           -- preview: what a sync would push (at most 4 MiB)
    added       INTEGER NOT NULL DEFAULT 0,         -- tags installed that weren't there
    updated     INTEGER NOT NULL DEFAULT 0,
    removed     INTEGER NOT NULL DEFAULT 0,
    unchanged   INTEGER NOT NULL DEFAULT 0,
    recorded    INTEGER NOT NULL DEFAULT 0,         -- unchanged tags recorded as applied for the first time
    drift       TEXT NOT NULL DEFAULT '',           -- drift check: the tags that differ
    started_at  TEXT NOT NULL,
    finished_at TEXT
) STRICT;
CREATE INDEX routing_syncs_router ON routing_syncs(router_id, id);
CREATE INDEX routing_syncs_job ON routing_syncs(job_id);

-- Test connection runs, of a saved router or of the add form (3e).
CREATE TABLE routing_tests (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    router_id       INTEGER REFERENCES routing_routers(id) ON DELETE CASCADE, -- NULL: the add form
    form            TEXT NOT NULL,                  -- JSON routers.Connection: what was tested
    job_id          INTEGER,
    state           TEXT NOT NULL CHECK (state IN ('running','passed','warned','failed','confirm')),
    checks          TEXT NOT NULL DEFAULT '[]',     -- JSON [{"name":…,"ok":…,"detail":…}]
    confirm_hop     TEXT NOT NULL DEFAULT '' CHECK (confirm_hop IN ('','jump','router')),
    confirm_address TEXT NOT NULL DEFAULT '',
    confirm_key     TEXT NOT NULL DEFAULT '',       -- authorized_keys form of the key to pin
    confirm_fp      TEXT NOT NULL DEFAULT '',
    version         TEXT NOT NULL DEFAULT '',
    board           TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    finished_at     TEXT,
    CHECK ((state = 'confirm') = (confirm_hop <> ''))
) STRICT;

-- A hosted Shadowrocket config (3d).
CREATE TABLE routing_shadowrocket (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL UNIQUE,             -- also the file name: <name>.conf
    list_id       INTEGER NOT NULL REFERENCES routing_lists(id),
    policy        TEXT NOT NULL DEFAULT 'PROXY',
    token         BLOB NOT NULL,                    -- vault "shadowrocket:<id>:token"
    token_lookup  BLOB NOT NULL UNIQUE,             -- vault.Lookup(token)
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    created_at    TEXT NOT NULL,
    last_fetch_at TEXT,
    last_fetch_ua TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE routing_shadowrocket_versions (
    config_id  INTEGER NOT NULL REFERENCES routing_shadowrocket(id) ON DELETE CASCADE,
    number     INTEGER NOT NULL,                    -- 1, 2, …
    content    TEXT NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (config_id, number)
) STRICT;

CREATE TABLE routing_shadowrocket_fetches (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    config_id  INTEGER NOT NULL REFERENCES routing_shadowrocket(id) ON DELETE CASCADE,
    at         TEXT NOT NULL,
    ip         TEXT NOT NULL,
    user_agent TEXT NOT NULL DEFAULT ''             -- at most 256 characters
) STRICT;
CREATE INDEX routing_shadowrocket_fetches_config ON routing_shadowrocket_fetches(config_id, at);
CREATE INDEX routing_shadowrocket_fetches_at ON routing_shadowrocket_fetches(at);

-- An mtvpn import: its preview, then its result (3d). Never the file's text.
CREATE TABLE routing_imports (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    state        TEXT NOT NULL CHECK (state IN ('preview','running','done','failed')),
    list_id      INTEGER REFERENCES routing_lists(id) ON DELETE SET NULL,
    rows         TEXT NOT NULL DEFAULT '[]',        -- JSON mtvpn.Row: selector, tag, from, status, choice, then outcome
    ignored      TEXT NOT NULL DEFAULT '',          -- the ignored keys' names, comma-joined
    base         TEXT NOT NULL DEFAULT '',          -- the fetched shadowrocket_base
    base_url     TEXT NOT NULL DEFAULT '',
    shadowrocket TEXT NOT NULL DEFAULT '{}',        -- JSON: the config choice, then its outcome
    job_id       INTEGER,
    created_at   TEXT NOT NULL,
    finished_at  TEXT
) STRICT;

-- Discovery (3g).
CREATE TABLE routing_discovery_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    input       TEXT NOT NULL,                      -- as typed
    url         TEXT NOT NULL,                      -- what is visited
    host        TEXT NOT NULL,
    registrable TEXT NOT NULL,
    via         TEXT NOT NULL CHECK (via IN ('direct','server','auto')),
    server_id   INTEGER,                            -- the servers module's id, no foreign key
    server_name TEXT NOT NULL DEFAULT '',
    depth       INTEGER NOT NULL DEFAULT 0 CHECK (depth IN (0,5)),
    state       TEXT NOT NULL CHECK (state IN ('queued','running','done','failed')),
    job_id      INTEGER,
    title       TEXT NOT NULL DEFAULT '',
    suggestions TEXT NOT NULL DEFAULT '[]',         -- JSON catalog.Suggestion
    visits      TEXT NOT NULL DEFAULT '[]',         -- JSON discovery.VisitRecord
    capped      INTEGER NOT NULL DEFAULT 0 CHECK (capped IN (0,1)),
    error       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    finished_at TEXT,
    CHECK (via <> 'server' OR server_id IS NOT NULL)
) STRICT;

CREATE TABLE routing_discovered_hosts (
    run_id      INTEGER NOT NULL REFERENCES routing_discovery_runs(id) ON DELETE CASCADE,
    host        TEXT NOT NULL,                      -- a hostname or an IP literal
    registrable TEXT NOT NULL DEFAULT '',           -- '' for an IP literal
    class       TEXT NOT NULL CHECK (class IN ('first-party','cdn','tracker','third-party','ip')),
    requests    INTEGER NOT NULL,
    failed      TEXT NOT NULL DEFAULT '',           -- the failure on the direct visit; '' none
    seen        TEXT NOT NULL DEFAULT '',           -- the visits that saw it: "direct, nl-1"
    PRIMARY KEY (run_id, host)
) STRICT;

-- +goose Down
DROP TABLE routing_discovered_hosts;
DROP TABLE routing_discovery_runs;
DROP TABLE routing_imports;
DROP TABLE routing_shadowrocket_fetches;
DROP TABLE routing_shadowrocket_versions;
DROP TABLE routing_shadowrocket;
DROP TABLE routing_tests;
DROP TABLE routing_syncs;
DROP TABLE routing_router_unmanaged;
DROP TABLE routing_router_tags;
DROP TABLE routing_routers;
DROP TABLE routing_catalog_includes;
DROP TABLE routing_catalog_domains;
DROP TABLE routing_catalog_entries;
DROP TABLE routing_catalog_sources;
DROP TABLE routing_list_services;
DROP TABLE routing_lists;
DROP TABLE routing_custom_domains;
DROP TABLE routing_snapshots;
DROP TABLE routing_services;
