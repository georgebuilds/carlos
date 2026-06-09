---
name: calendar
description: Entry point for any calendar request. Picks the right backend skill based on the active frame's capability config.
triggers: [calendar, schedule, meeting, event, agenda]
---

# calendar

You handle anything calendar-shaped: listing events, scheduling meetings, finding a free slot, canceling, rescheduling, or rendering an agenda. You do not store events yourself. You delegate to one of four backend skills based on configuration.

## Backends

There are four backends. Each is a sibling skill in this bundle.

- `calendar-ics-file` reads and writes a local `.ics` file. Best for offline use and single-user vaults.
- `calendar-caldav` speaks CalDAV to Fastmail, iCloud, Nextcloud, or any RFC 4791 server. Best for shared work calendars.
- `calendar-apple` drives Calendar.app on macOS via `osascript`. Best when the user already keeps everything in Apple's stack.
- `calendar-mcp` calls an MCP calendar server. Best when the user has wired one up and wants provider-neutral access.

## Picking the backend

The active frame's config carries a `capabilities.calendar.<frame>.backend` field. Read it from the runtime config and route as follows.

| `backend` value | Delegate to        |
|-----------------|--------------------|
| `ics`           | `calendar-ics-file` |
| `caldav`        | `calendar-caldav`   |
| `apple`         | `calendar-apple`    |
| `mcp`           | `calendar-mcp`      |

If the field is unset, ask the user once which backend they want and offer to write the chosen value into config. Do not guess.

If the request explicitly says "across all frames" or "everything on my calendar today" without naming a frame, delegate to `calendar-cross-frame-view` instead. That skill fans out across every configured frame and merges the results.

## Output shape

When you (or the delegated skill) return events to the user, use a compact agenda format sorted by start time:

```
HH:MM-HH:MM  SUMMARY              LOCATION
```

Drop the location column when empty. Use the frame's glyph + name as a prefix when more than one frame is in play.
