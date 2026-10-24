# Connect Google Calendar or Microsoft 365

hima talks to Google and Microsoft through an OAuth app that **you** register. It takes about ten minutes, once. hima then reads only your **free/busy** time (never event titles) and, if you choose, writes your Bookings into one calendar. Why it works this way: [ADR 0004](adr/0004-oauth-calendars-via-owner-registered-apps.md).

You need `base_url` set to hima's public HTTPS address: the redirect URI is built from it, and hima shows the exact URI on **Admin → Calendar sources**.

## Google Calendar

1. Open [Google Cloud Console](https://console.cloud.google.com/), create a project (e.g. "hima").
2. **APIs & Services → Library**: enable **Google Calendar API**.
3. **OAuth consent screen**: user type *External*, fill in the app name and your email. Under **Test users**, add your own Google account. While the app is in *Testing*, Google expires its tokens after 7 days, so you'd have to reconnect weekly. Switching it to *In production* avoids that; for calendar scopes Google then shows an "unverified app" warning you can click through for your own account. Check Google's current verification rules if you plan to let others connect.
4. **Credentials → Create credentials → OAuth client ID**: type *Web application*; add the authorised redirect URI
   `https://hima.example.com/admin/oauth/google/callback`.
5. Put the client ID and secret into `hima.yaml`:

   ```yaml
   base_url: https://hima.example.com
   google:
     client_id: 1234-abc.apps.googleusercontent.com
     client_secret: GOCSPX-…
     read: [primary]          # calendar IDs to treat as busy; [] for none
     write: primary           # optional: write Bookings here
   ```

6. Restart hima, open **Admin → Calendar sources**, click **Connect Google Calendar**, and approve.

Other calendars' IDs are under *Google Calendar → Settings → (calendar) → Integrate calendar → Calendar ID*.

## Microsoft 365 / Outlook.com

1. Open [Microsoft Entra admin center → App registrations](https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade) → **New registration**.
   - Supported account types: *Personal Microsoft accounts and any organisational directory* (or only your organisation).
   - Redirect URI: platform *Web*, `https://hima.example.com/admin/oauth/microsoft/callback`.
2. **Certificates & secrets → New client secret**; copy the *value*. (It expires; set a reminder to renew it.)
3. **API permissions → Add → Microsoft Graph → Delegated**: `Calendars.Read` (or `Calendars.ReadWrite` to write Bookings), `User.Read`, `offline_access`.
4. In `hima.yaml`:

   ```yaml
   base_url: https://hima.example.com
   microsoft:
     client_id: 00000000-0000-0000-0000-000000000000   # Application (client) ID
     client_secret: "…"
     tenant: common            # or your Directory (tenant) ID
     read: true                # use your mailbox's free/busy
     write: false              # true: Bookings go to your default calendar
   ```

5. Restart, then **Admin → Calendar sources → Connect Microsoft 365**.

## Notes

- Bookings are written to **one** calendar in total: CalDAV, Google or Microsoft.
- Until you click Connect, a configured provider counts as having no busy time; the admin shows it as *Not connected*.
- *Disconnect* forgets hima's tokens. Also remove the app's access in your [Google account](https://myaccount.google.com/permissions) or [Microsoft account](https://account.live.com/consent/Manage).
- `all_day_blocks` doesn't apply here: the event's "show as busy/free" setting decides.
