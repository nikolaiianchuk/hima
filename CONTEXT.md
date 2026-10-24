# hima (暇 — "free time")

A self-hosted scheduling tool: one person shares expiring links so friends can book time against their free/busy calendar, without emails or accounts.

## Language

### People

**Owner**:
The single person who runs an instance and whose calendar is exposed as free/busy.
_Avoid_: User, host, admin

**Guest**:
A person holding an Invite who books time with the Owner; never has an account.
_Avoid_: Friend, user, attendee, invitee

**Guest name**:
The name a Guest gives when making a Booking, pre-filled from the Invite label.
_Avoid_: Username, display name

### Scheduling

**Invite**:
A shareable, expiring link the Owner creates that lets a Guest make Bookings, limited by expiry, use count, allowed durations, date range and a label.
_Avoid_: Link, token, booking link

**Invite state**:
One of Active (can create Bookings), Used up (use count reached), Expired (validity passed) or Revoked (killed early by the Owner).
_Avoid_: Disabled, deleted, closed

**Label**:
The Owner's private note on an Invite saying who or what it is for; never shown as the Guest's identity.
_Avoid_: Invite name, guest name

**Location**:
Where a Booking takes place — a place, a call, or a video link — set by the Owner on the Invite.
_Avoid_: Venue, meeting URL

**Booking**:
A reserved time slot with the Owner, created by a Guest through an Invite.
_Avoid_: Meeting, event, appointment

**Manage link**:
The private URL a Guest receives for one Booking, used to reschedule or cancel it.
_Avoid_: Meeting id, edit link

**Cancellation**:
The ending of a Booking before it happens, recording whether the Guest or the Owner cancelled it.
_Avoid_: Deletion, guest-cancel / owner-cancel as separate terms

**Owner feed**:
A secret calendar subscription URL, published by the instance, listing all Bookings so they appear in the Owner's own calendar.
_Avoid_: Sync, export, Calendar source


**Calendar source**:
An external calendar the Owner's busy time is read from.
_Avoid_: Integration, connected calendar, feed (reserved for what we publish)

**Busy block**:
A time range during which the Owner is taken, known only by its start and end.
_Avoid_: Event, appointment

**Availability**:
The Owner's standing rules for when they can be booked: weekly hours, time zone, buffers, minimum notice and booking horizon.
_Avoid_: Working hours, schedule

**Slot**:
A time a Guest may book: within Availability, within the Invite's limits, and overlapping no Busy block.
_Avoid_: Free time, opening

## Relationships

- An instance has exactly one **Owner**
- A **Guest** is known only through the **Invite** they hold
- An **Invite** produces zero or more **Bookings**, up to its use count
- A **Booking** has exactly one **Manage link**
- Rescheduling changes an existing **Booking**; it does not consume an **Invite** use
- A **Manage link** lives as long as its **Booking**, regardless of the **Invite** expiring; once the **Booking** has ended it is read-only, and it stops working 30 days later
- Cancelling a **Booking** does not give the use back to the **Invite**
- Revoking an **Invite** stops new **Bookings** only; existing **Bookings** survive
- Guests cannot tell a Used up, Expired or Revoked **Invite** apart
- An Active **Invite**'s expiry, use count and **Label** may change after sharing; its durations, date range and **Location** may not
- A **Guest** sees only **Slots** by default; an **Invite** may opt in to also showing **Busy blocks**, and never beyond its own date range
- **Busy blocks** come from **Calendar sources** and from existing **Bookings**; a new **Booking** is a **Busy block** immediately
- A **Booking** takes its **Location** from its **Invite**; the **Guest** supplies a **Guest name** and optional note, never the **Location**
- Only the **Guest** reschedules a **Booking** (via the **Manage link**); the **Owner** may cancel it or edit its location and notes, but not move it
- There is exactly one **Owner feed**; Guests have no feed and get a one-off calendar file plus their **Manage link**
- **Busy blocks** are the union of every **Calendar source**; if any source cannot be trusted as current, no **Slots** are offered
- Events marked free, cancelled, or declined by the **Owner** in a **Calendar source** never become **Busy blocks**; all-day events do only if that source says so
- The **Owner** is notified of new **Bookings**, reschedules, Guest **Cancellations** and failing **Calendar sources**; a failed notification never undoes a **Booking**
- There is one **Availability**; an **Invite** may narrow it but never widen it
- **Availability** is wall-clock time in the **Owner**'s time zone; a **Booking** is a fixed instant that never shifts when that zone changes

## Example dialogue

> **Dev:** "Alex's **Invite** expired yesterday — can they still move their **Booking**?"
> **Owner:** "Yes, through the **Manage link**. The **Invite** only controls creating new **Bookings**."
> **Dev:** "And if they cancel, can they reuse the **Invite**?"
> **Owner:** "No. I'll send a fresh **Invite** if I want them to book again."

## Flagged ambiguities

- **Label** vs **Guest name**: usually equal ("Alex"), but a shared Invite labelled "climbing group" yields Bookings from "Maria", "Tom" — keep them distinct.
- "feed" vs "calendar": a **Calendar source** is read by the instance; the **Owner feed** is published by it. Never call either the other.

- "friend" was used for the people booking — resolved: they are **Guests**; "friend" describes the social relationship, not a role in the system.
- "meeting id" was proposed for changing a Booking's time — resolved: it's the **Manage link**, a secret URL, not a guessable identifier.
