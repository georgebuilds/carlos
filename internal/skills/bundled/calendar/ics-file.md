---
name: calendar-ics-file
description: Read, write, and search a local .ics calendar file. Default backend for the personal frame.
backend: ics
frame_default: personal
triggers: [calendar, ics, agenda, event, schedule]
---

# calendar-ics-file

You operate on a single `.ics` file at the path in `capabilities.calendar.<frame>.path`. The file is plain text, RFC 5545 (iCalendar).

## File structure

The file begins with `BEGIN:VCALENDAR` / `VERSION:2.0` / `PRODID:...` and ends with `END:VCALENDAR`. Events sit between as `VEVENT` blocks.

A minimal event:

```
BEGIN:VEVENT
UID:2026-06-06T1500-standup@carlos
DTSTAMP:20260606T120000Z
DTSTART;TZID=America/New_York:20260606T150000
DTEND;TZID=America/New_York:20260606T153000
SUMMARY:Standup
DESCRIPTION:Daily sync
LOCATION:Zoom
ATTENDEE;CN=Alice:mailto:alice@example.com
END:VEVENT
```

Rules:

- `DTSTART` and `DTEND` must use `;TZID=<IANA zone>` when the event has a local time. Use a `Z`-suffixed UTC form only for all-day-UTC events.
- `UID` must be unique across the whole file. Use the pattern `<iso-timestamp>-<slug>@carlos` if the user does not supply one.
- `RRULE` carries recurrence: e.g. `RRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR;UNTIL=20260801T000000Z`.
- Lines longer than 75 octets must be folded with a CRLF + single space. Avoid generating them when you can keep lines short.

## Verbs

### List today's events

Use `read` to load the file, then filter by `DTSTART` matching today in the user's local zone.

```
tool: read
args: { "path": "<config.path>" }
```

Parse out every `VEVENT` block, keep those whose `DTSTART` date matches today, sort by start time, render in the agenda format from the index skill.

### Add an event tomorrow at 3pm

1. `read` the file to capture the current contents and pick a unique UID.
2. `edit` the file: insert the new `VEVENT` block immediately before the final `END:VCALENDAR` line. Use a unique `old_string` like `END:VCALENDAR\n` (it appears once).

```
tool: edit
args:
  path: <config.path>
  old_string: "END:VCALENDAR\n"
  new_string: "BEGIN:VEVENT\nUID:20260607T150000-meeting@carlos\nDTSTAMP:20260606T120000Z\nDTSTART;TZID=America/New_York:20260607T150000\nDTEND;TZID=America/New_York:20260607T160000\nSUMMARY:Meeting\nEND:VEVENT\nEND:VCALENDAR\n"
```

When the file is empty or absent, `write` a full `VCALENDAR` wrapper around the single new `VEVENT`.

### Search events

Use `read` to load the file, then string-match `SUMMARY`, `DESCRIPTION`, or `LOCATION` lines. For large vaults (over a few hundred events), prefer `grep` on the file with a `BEGIN:VEVENT/END:VEVENT` context window of `-A 20`.

### Delete an event

`edit` the file with `old_string` set to the complete `VEVENT` block (including the trailing newline) and `new_string` empty.

## Gotchas

- Never duplicate a `UID`. If the user re-adds the "same" event, generate a fresh UID.
- Times without `TZID` are treated as floating by other clients. Always set `TZID` unless the user asks for a true UTC event.
- All-day events use `DTSTART;VALUE=DATE:20260607` (no time, no zone).
