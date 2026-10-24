CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- What happened, for the admin's activity log. Times are unix seconds.
CREATE TABLE activity (
    id      INTEGER PRIMARY KEY,
    at      INTEGER NOT NULL,
    kind    TEXT    NOT NULL,
    title   TEXT    NOT NULL,
    message TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX activity_at ON activity(at);
