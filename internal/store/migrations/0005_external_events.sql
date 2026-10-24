-- The ID a calendar provider gave a written-back Booking, where hima can't
-- choose it (Microsoft Graph).
CREATE TABLE external_events (
    booking_id INTEGER NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    provider   TEXT    NOT NULL,
    remote_id  TEXT    NOT NULL,
    PRIMARY KEY (booking_id, provider)
);
