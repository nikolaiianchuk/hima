# Email invitations

With `email_invites` set, hima emails **you** a calendar invitation for every Booking: a new invitation when a Guest books, an update when they reschedule or you edit it, and a cancellation when it's cancelled. Gmail, Outlook, Apple Mail, Fastmail and most other mail apps add these to your calendar by themselves. No calendar API or OAuth app is involved, only an SMTP account.

Guests are never emailed. This only goes to the address you configure.

It counts as your one write-back calendar, so don't combine it with `caldav.write`, `google.write` or `microsoft.write`.

## Setup

```yaml
owner:
  name: Nikolai
  emails: [nikolai@example.com]     # invitations go to the first one

email_invites:
  from: hima@example.com            # must not be your own address
  smtp:
    host: smtp.fastmail.com
    port: 465                       # 465 = TLS from the start; 587 = STARTTLS (default)
    username: hima@example.com
    password_file: /run/secrets/smtp   # or password: "…"
```

Set `email_invites.to` if invitations should go somewhere other than `owner.emails[0]`.

Common SMTP settings (use an **app password**, not your normal one):

| Provider | host | port | username |
|---|---|---|---|
| Fastmail | `smtp.fastmail.com` | 465 | your address |
| Gmail / Google Workspace | `smtp.gmail.com` | 465 | your address |
| iCloud Mail | `smtp.mail.me.com` | 587 | your iCloud address |
| Outlook.com | `smtp-mail.outlook.com` | 587 | your address |
| Postmark, SES, Mailgun, … | their SMTP host | 587 | their SMTP credentials |

## Making sure invitations are added automatically

- **Use a sender your provider trusts.** `from` should be an address your SMTP account may send as (same domain, so SPF and DKIM pass). Invitations from a self-run mail server without SPF/DKIM often land in spam and are never added.
- **The sender must differ from you.** Calendars ignore invitations "from yourself". A second address or alias (e.g. `hima@yourdomain`) works well.
- **Gmail:** *Google Calendar → Settings → Event settings → Add invitations to my calendar*. "From everyone" adds them straight away; "Only if the sender is known" needs `from` in your contacts.
- **Outlook:** invitations are added as tentative until opened, depending on your settings.
- **Apple Mail / Calendar:** the invitation appears in Calendar's inbox; updates and cancellations apply by themselves after the first one is added.

## What you'll see

- The invitation comes from "hima", with you as an attendee who has already accepted and owes no reply, so there's no RSVP prompt to answer. If you do reply, the reply goes to `from` and hima ignores it.
- Each message also carries the event as `invite.ics`, and a plain-text summary for mail apps that don't understand invitations.
- Delivery failures are retried, then reported through your notifications and shown on **Admin → Calendar sources**.
