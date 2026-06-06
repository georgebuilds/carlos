---
name: calendar-mcp
description: Call an MCP calendar server when one is configured. Forward-compatible with the carlos MCP client landing post-v1.
backend: mcp
frame_default: research
triggers: [calendar, mcp, schedule, event, meeting]
---

# calendar-mcp

You call an MCP calendar server for the active frame. The carlos MCP client lands after v1; until then this skill is forward-compatible. If the expected tools are not registered yet, fall through to the setup guidance at the bottom of this file.

## Expected tools

When the MCP client is wired, these tools appear in the tool registry alongside `read`, `write`, etc. Names are stable across the ecosystem; treat them as the contract.

| Tool                          | Purpose                                          |
|-------------------------------|--------------------------------------------------|
| `mcp_calendar_list_events`    | Range query, returns events sorted by start time |
| `mcp_calendar_get_event`      | Single event by id                               |
| `mcp_calendar_create_event`   | Create one event                                 |
| `mcp_calendar_update_event`   | Patch one event by id                            |
| `mcp_calendar_delete_event`   | Remove one event by id                           |
| `mcp_calendar_search_events`  | Free-text search across summary + description    |

### List today

```
tool: mcp_calendar_list_events
args:
  start: 2026-06-06T00:00:00-04:00
  end:   2026-06-07T00:00:00-04:00
  calendar: <capabilities.calendar.<frame>.mcp_calendar>
```

### Create an event

```
tool: mcp_calendar_create_event
args:
  calendar: <capabilities.calendar.<frame>.mcp_calendar>
  summary: Meeting
  start: 2026-06-07T15:00:00-04:00
  end:   2026-06-07T16:00:00-04:00
  description: ""
  location: ""
  attendees: []
```

### Delete

```
tool: mcp_calendar_delete_event
args:
  id: <event id from list/get>
```

## When the tools are absent

If `mcp_calendar_list_events` is not in the registry, the user does not yet have an MCP calendar server configured. Do not fail silently. Tell the user:

1. Install an MCP calendar server. Examples in the ecosystem: `@modelcontextprotocol/server-google-calendar`, `mcp-server-caldav`, or any community implementation that exposes the tools listed above.
2. Add it to `~/.carlos/config.yaml` under `mcp.servers.<name>` with the command and env the server needs.
3. Restart carlos.
4. Set `capabilities.calendar.<frame>.backend: mcp` and `capabilities.calendar.<frame>.mcp_server: <name>` for the frame.

Offer to write the config block when the user confirms which server they want. Do not pick one for them.

## Gotchas

- Tool names are case-sensitive and use underscores, not dashes.
- All times are ISO 8601 with explicit offset. Do not pass naive local times.
- Recurrence semantics vary by server. When listing, prefer expanded instances; when creating, accept `rrule` only if the server advertises it in its tool schema.
