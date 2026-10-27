CREATE TABLE proxies (
    id               bigserial PRIMARY KEY,
    kind             text        NOT NULL CHECK (kind IN ('direct','http','https')),
    host             text,
    port             int,
    username         text,
    password_enc     bytea,
    status           text        NOT NULL DEFAULT 'active'
                     CHECK (status IN ('active','disabled','dead')),
    max_accounts     int,
    fail_count       int         NOT NULL DEFAULT 0,
    cooldown_until   timestamptz,
    cooldown_count   int         NOT NULL DEFAULT 0,
    last_ok_at       timestamptz,
    last_error       text,
    last_error_at    timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'direct') = (host IS NULL)),
    UNIQUE (kind, host, port, username)
);
CREATE UNIQUE INDEX proxies_one_direct ON proxies (kind) WHERE kind = 'direct';
INSERT INTO proxies (kind) VALUES ('direct');

CREATE TABLE steam_accounts (
    id                bigserial PRIMARY KEY,
    username          text        NOT NULL UNIQUE,
    password_enc      bytea       NOT NULL,
    token_enc         bytea,
    shared_secret_enc bytea,
    guard_type        text        CHECK (guard_type IN ('email','mobile','device_confirm')),
    proxy_id          bigint      REFERENCES proxies(id),
    pinned_proxy      boolean     NOT NULL DEFAULT false,
    steam_id          bigint      UNIQUE,
    others_history    boolean,
    status            text        NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','disabled','auth_failed','guard_required','throttled','banned')),
    gc_daily_limit    int         NOT NULL DEFAULT 100,
    gc_remaining      int         NOT NULL DEFAULT 100 CHECK (gc_remaining >= 0),
    gc_reset_at       timestamptz NOT NULL DEFAULT now() + interval '24 hours',
    cooldown_until    timestamptz,
    last_login_at     timestamptz,
    last_error        text,
    last_error_at     timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX steam_accounts_ready ON steam_accounts (gc_remaining DESC) WHERE status = 'active';

CREATE TABLE players (
    account_id       bigint      PRIMARY KEY,
    history_private  boolean     NOT NULL DEFAULT false,
    last_match_id    bigint,
    checked_at       timestamptz,
    discovered_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX players_due ON players (checked_at NULLS FIRST) WHERE NOT history_private;

CREATE TABLE matches (
    match_id         bigint      PRIMARY KEY,
    source           text        NOT NULL,
    details_at       timestamptz,
    cluster          int,
    replay_salt      bigint,
    replay_state     text,
    start_time       timestamptz,
    duration_s       int,
    game_mode        int,
    lobby_type       int,
    radiant_win      boolean,
    radiant_score    int,
    dire_score       int,
    average_rank     int,
    raw_details      bytea,
    discovered_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX matches_start_time ON matches (start_time);

CREATE TABLE match_players (
    match_id         bigint      NOT NULL REFERENCES matches(match_id),
    player_slot      int         NOT NULL,
    account_id       bigint,
    hero_id          int         NOT NULL,
    kills int, deaths int, assists int,
    last_hits int, denies int, gold_per_min int, xp_per_min int,
    net_worth int, hero_damage int, tower_damage int, hero_healing int,
    level int,
    items            bigint[],
    PRIMARY KEY (match_id, player_slot)
);
CREATE INDEX match_players_account ON match_players (account_id);

CREATE TABLE replays (
    match_id         bigint      PRIMARY KEY REFERENCES matches(match_id),
    url              text        NOT NULL,
    data             bytea       NOT NULL,
    compression      text        NOT NULL CHECK (compression IN ('zstd','bzip2')),
    size_bytes       bigint      NOT NULL,
    sha256           bytea       NOT NULL,
    downloaded_at    timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE replays ALTER COLUMN data SET STORAGE EXTERNAL;

CREATE TABLE app_state (
    key              text        PRIMARY KEY,
    value            text        NOT NULL,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tasks (
    id               bigserial   PRIMARY KEY,
    kind             text        NOT NULL CHECK (kind IN ('webapi_sequence','top_live_games','player_history',
                                                         'match_details','replay_download','proxy_check')),
    target_id        bigint,
    status           text        NOT NULL DEFAULT 'queued'
                     CHECK (status IN ('queued','running','done','skipped','failed','cancelled')),
    priority         int         NOT NULL DEFAULT 0,
    run_after        timestamptz NOT NULL DEFAULT now(),
    attempts         int         NOT NULL DEFAULT 0,
    max_attempts     int         NOT NULL DEFAULT 5,
    account_id       bigint      REFERENCES steam_accounts(id),
    proxy_id         bigint      REFERENCES proxies(id),
    parent_id        bigint      REFERENCES tasks(id),
    last_error       text,
    created_by       text        NOT NULL DEFAULT 'system',
    created_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tasks_one_active ON tasks (kind, (coalesce(target_id, 0))) WHERE status IN ('queued','running');
CREATE INDEX tasks_queue ON tasks (kind, priority DESC, run_after) WHERE status = 'queued';
CREATE INDEX tasks_status_time ON tasks (status, updated_at DESC);
CREATE INDEX tasks_target ON tasks (kind, target_id);

CREATE TABLE gc_requests (
    id               bigserial   PRIMARY KEY,
    account_id       bigint      NOT NULL REFERENCES steam_accounts(id),
    proxy_id         bigint      NOT NULL REFERENCES proxies(id),
    task_id          bigint      REFERENCES tasks(id),
    kind             text        NOT NULL,
    target_id        bigint,
    result           text        NOT NULL,
    eresult          int,
    latency_ms       int,
    requested_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX gc_requests_account_time ON gc_requests (account_id, requested_at);
CREATE INDEX gc_requests_time ON gc_requests (requested_at);
