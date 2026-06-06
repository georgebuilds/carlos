---
name: calendar-cross-frame-view
description: Opt-in unified next-24-hours agenda. Fans out to every frame's configured calendar backend and merges the results.
backend: any
triggers: [calendar, agenda, everything, all calendars, unified]
---

# calendar-cross-frame-view

You produce one merged agenda spanning every configured frame. Use this when the user asks for "everything", "the full day", "all my calendars", or names more than one frame.

This skill never reads a backend directly. It delegates to the per-frame sibling skills and merges their output.

## Fan-out pattern

1. Read the frames list from `frames.list` in config.
2. For each frame `f`, look up `capabilities.calendar.<f.name>.backend`.
   - `ics` -> delegate to `calendar-ics-file` with `<f.name>` as the active frame.
   - `caldav` -> delegate to `calendar-caldav`.
   - `apple` -> delegate to `calendar-apple`.
   - `mcp` -> delegate to `calendar-mcp`.
   - Empty or unknown -> skip this frame.
3. For each delegate, request events in the window `[now, now + 24h]` in the user's local zone.
4. Tag each returned event with the frame's `name` and `glyph` from the config (defaults from `internal/frame.DefaultGlyphFor`).
5. Merge the lists, sort by `start`, and render.

Run the delegations in parallel when the runtime supports it. The backends are independent.

## Render

Use one line per event. Prefix with the frame glyph + name so the user can see which calendar an event came from. The accent color comes from `frame.AccentColor(f.accent)` and is applied by the renderer, not by this skill's output text.

```
◉ personal  09:00-09:30  Standup            Zoom
▣ work      10:00-11:00  Sprint planning    Hangouts
◈ research  14:00-15:00  Reading group      -
```

When two events from different frames overlap, keep both lines and let the renderer style the conflict.

## Empty frames

If a frame has no events in the next 24 hours, omit it from the output rather than printing an empty header. Mention skipped frames only when the user explicitly asks "did you check work too?".

## Errors

If a backend errors (CalDAV 401, missing ICS file, Calendar.app TCC denial), record the failure and surface it as a footer line:

```
! work: caldav 401 (check FASTMAIL_APP_PASSWORD)
```

Do not abort the whole merge for one bad backend. Show what you can.

## When not to use this skill

If the user is in a specific frame and asks for "my calendar today", use the per-frame backend skill directly. This skill is the fan-out path only.
