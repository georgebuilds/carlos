---
name: calendar-caldav
description: Talk to a CalDAV server (Fastmail, iCloud, Nextcloud) for the active frame. Default backend for work.
backend: caldav
frame_default: work
triggers: [calendar, caldav, schedule, event, meeting]
---

# calendar-caldav

You speak CalDAV (RFC 4791) over HTTPS. Read events, create events, delete events. You do not manage subscriptions or sharing.

## Config keys

Read these from the active frame's capability map.

| Key                                              | Example                                                   |
|--------------------------------------------------|-----------------------------------------------------------|
| `capabilities.calendar.<frame>.caldav_url`       | `https://caldav.fastmail.com/dav/calendars/user/me/work/` |
| `capabilities.calendar.<frame>.caldav_user_env`  | `FASTMAIL_USER`                                           |
| `capabilities.calendar.<frame>.caldav_pass_env`  | `FASTMAIL_APP_PASSWORD`                                   |

Resolve the user and password from environment variables, never from disk. If either env var is unset, stop and tell the user which one to populate.

Servers known to work with this skill:

- Fastmail (`caldav.fastmail.com`)
- iCloud (`caldav.icloud.com`, app-specific password required)
- Nextcloud (`/remote.php/dav/calendars/<user>/<cal>/`)

## Verbs

### List events in a time range

Send a `REPORT` with depth 1 and a `calendar-query` body. Content type must be `application/xml; charset=utf-8`.

```
tool: http_request
args:
  method: REPORT
  url: <caldav_url>
  headers:
    Content-Type: application/xml; charset=utf-8
    Depth: "1"
  basic_auth:
    user: <env caldav_user_env>
    pass: <env caldav_pass_env>
  body: |
    <?xml version="1.0" encoding="utf-8" ?>
    <c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
      <d:prop>
        <d:getetag/>
        <c:calendar-data/>
      </d:prop>
      <c:filter>
        <c:comp-filter name="VCALENDAR">
          <c:comp-filter name="VEVENT">
            <c:time-range start="20260606T000000Z" end="20260607T000000Z"/>
          </c:comp-filter>
        </c:comp-filter>
      </c:filter>
    </c:calendar-query>
```

The response is XML (multistatus) containing one `calendar-data` per matching event. Parse out the inner `VEVENT` blocks and render with the agenda format from the index skill. The `etag` on each response item is needed for safe updates and deletes; remember it.

### Create an event

`PUT` a single-VEVENT `.ics` document to a fresh URL under the calendar collection. The URL path component is the UID with a `.ics` suffix.

```
tool: http_request
args:
  method: PUT
  url: <caldav_url><uid>.ics
  headers:
    Content-Type: text/calendar; charset=utf-8
    If-None-Match: "*"
  basic_auth:
    user: <env caldav_user_env>
    pass: <env caldav_pass_env>
  body: |
    BEGIN:VCALENDAR
    VERSION:2.0
    PRODID:-//carlos//calendar-caldav//EN
    BEGIN:VEVENT
    UID:<uid>
    DTSTAMP:20260606T120000Z
    DTSTART;TZID=America/New_York:20260607T150000
    DTEND;TZID=America/New_York:20260607T160000
    SUMMARY:Meeting
    END:VEVENT
    END:VCALENDAR
```

`If-None-Match: "*"` makes the create safe against a UID collision. On `412 Precondition Failed`, pick a new UID and retry.

### Delete an event

`DELETE` the event resource. Include the last known `etag` as `If-Match` so a stale delete fails loudly.

```
tool: http_request
args:
  method: DELETE
  url: <caldav_url><uid>.ics
  headers:
    If-Match: "<etag>"
  basic_auth:
    user: <env caldav_user_env>
    pass: <env caldav_pass_env>
```

### Update an event

`PUT` to the same URL with `If-Match: <etag>`. The body is a full single-VEVENT `.ics` document, same shape as create.

## Gotchas

- The body content-type for `REPORT` is `application/xml; charset=utf-8`. For `PUT` it is `text/calendar; charset=utf-8`. Mixing them up is the most common failure.
- iCloud requires an app-specific password and a `Prefer: return=minimal` header on writes to suppress the full event in the response body.
- Some servers reject `Depth: infinity`. Always use `Depth: 1` on `REPORT`.
- Time-range filters compare against expanded recurring instances. A weekly meeting will appear in every matching week.
