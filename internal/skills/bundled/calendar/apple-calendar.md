---
name: calendar-apple
description: Drive Calendar.app on macOS via osascript. Reads, creates, deletes events in the user's local Calendar database.
backend: apple
frame_default: personal
triggers: [calendar, apple, ical, schedule, event, meeting]
---

# calendar-apple

You drive Calendar.app on macOS through `osascript`. This skill only works on darwin. If the host is Linux, stop and tell the user to switch backends.

Run every AppleScript snippet via the `bash` tool:

```
tool: bash
args:
  command: osascript -e '<applescript here>'
```

When the AppleScript contains single quotes, escape them as `'\''` so the outer `bash -c` boundary survives. Multi-line scripts use multiple `-e` arguments, one per line. Always quote the whole `osascript ...` invocation.

The default calendar is `capabilities.calendar.<frame>.apple_calendar`. Fall back to the first writable calendar if unset.

## Verbs

### List today's events

```
osascript \
  -e 'set startDate to current date' \
  -e 'set time of startDate to 0' \
  -e 'set endDate to startDate + 1 * days' \
  -e 'tell application "Calendar"' \
  -e '  set out to ""' \
  -e '  repeat with c in calendars' \
  -e '    set evs to (every event of c whose start date >= startDate and start date < endDate)' \
  -e '    repeat with e in evs' \
  -e '      set out to out & (start date of e as string) & " | " & summary of e & linefeed' \
  -e '    end repeat' \
  -e '  end repeat' \
  -e '  return out' \
  -e 'end tell'
```

Parse the stdout, sort by start time, and render with the agenda format from the index skill.

### List next 7 days

Same shape, but `set endDate to startDate + 7 * days`.

### Create an event tomorrow at 3pm

```
osascript \
  -e 'set s to (current date) + 1 * days' \
  -e 'set time of s to 15 * hours' \
  -e 'set e to s + 1 * hours' \
  -e 'tell application "Calendar"' \
  -e '  tell calendar "Personal"' \
  -e '    set newE to make new event with properties {summary:"Meeting", start date:s, end date:e}' \
  -e '    return uid of newE' \
  -e '  end tell' \
  -e 'end tell'
```

Capture the returned UID; the user may want it for follow-up edits or deletes.

### Delete an event by UID

```
osascript \
  -e 'tell application "Calendar"' \
  -e '  repeat with c in calendars' \
  -e '    set matches to (every event of c whose uid is "<UID>")' \
  -e '    repeat with m in matches' \
  -e '      delete m' \
  -e '    end repeat' \
  -e '  end repeat' \
  -e 'end tell'
```

Substitute `<UID>` literally. If the UID contains a double quote, fail loudly rather than try to escape it; legitimate UIDs do not.

## Gotchas

- AppleScript is line-sensitive. Each logical line goes in its own `-e` flag.
- Single quotes inside a `bash -c` body must be escaped as `'\''`. Avoid them when you can rewrite the script to use double quotes inside AppleScript string literals.
- The first call after a fresh user blocks on a TCC prompt ("carlos wants to access Calendar"). Surface that to the user rather than retrying silently.
- `current date` reflects the system clock. The user's timezone is implicit. Do not pass UTC offsets.
- Calendar.app does not have a true "search" verb; filter with the AppleScript `whose` clause, not regex.
