# Single Owner per instance

Each deployment serves exactly one Owner; Guests never get accounts. We chose this over a multi-host instance (Cal.com-style) to keep auth to a single login and every Booking implicitly "with the Owner". Group scheduling, if ever needed, is meant to come from separate instances exchanging free/busy, not from one instance holding several people's calendars.
