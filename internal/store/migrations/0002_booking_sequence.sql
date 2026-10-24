-- Incremented on every reschedule so calendars recognise updated events.
ALTER TABLE bookings ADD COLUMN sequence INTEGER NOT NULL DEFAULT 0;
