-- Times are unix seconds, UTC.

CREATE TABLE invites (
    id          INTEGER PRIMARY KEY,
    token_hash  TEXT    NOT NULL UNIQUE,
    label       TEXT    NOT NULL DEFAULT '',
    location    TEXT    NOT NULL DEFAULT '',
    durations   TEXT    NOT NULL,            -- comma-separated minutes, e.g. "30,60"
    max_uses    INTEGER NOT NULL DEFAULT 1,
    expires_at  INTEGER NOT NULL,
    range_start INTEGER,                     -- optional narrowing of Availability
    range_end   INTEGER,
    show_busy   INTEGER NOT NULL DEFAULT 0,
    revoked_at  INTEGER,
    created_at  INTEGER NOT NULL
);

CREATE TABLE bookings (
    id                INTEGER PRIMARY KEY,
    invite_id         INTEGER NOT NULL REFERENCES invites(id),
    manage_token_hash TEXT    NOT NULL UNIQUE,
    guest_name        TEXT    NOT NULL,
    note              TEXT    NOT NULL DEFAULT '',
    location          TEXT    NOT NULL DEFAULT '',
    starts_at         INTEGER NOT NULL,
    ends_at           INTEGER NOT NULL,
    cancelled_at      INTEGER,
    cancelled_by      TEXT CHECK (cancelled_by IN ('guest', 'owner')),
    created_at        INTEGER NOT NULL
);

CREATE INDEX bookings_invite ON bookings(invite_id);
CREATE INDEX bookings_time ON bookings(starts_at, ends_at);

CREATE TABLE sessions (
    token_hash TEXT    PRIMARY KEY,
    expires_at INTEGER NOT NULL
);
