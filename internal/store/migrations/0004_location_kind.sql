-- What kind of Location an Invite offers: '' (none), 'place', 'phone' or
-- 'video'. Guests see the kind before booking and the value after.
ALTER TABLE invites ADD COLUMN location_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE bookings ADD COLUMN location_kind TEXT NOT NULL DEFAULT '';
UPDATE invites SET location_kind = CASE
    WHEN location LIKE 'http%' THEN 'video' WHEN location != '' THEN 'place' ELSE '' END;
UPDATE bookings SET location_kind = CASE
    WHEN location LIKE 'http%' THEN 'video' WHEN location != '' THEN 'place' ELSE '' END;

-- Counts Guest reschedules; sequence also counts Owner edits.
ALTER TABLE bookings ADD COLUMN reschedules INTEGER NOT NULL DEFAULT 0;
