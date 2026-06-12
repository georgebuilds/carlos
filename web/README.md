# carlos web

The front-end SPA for `carlos web`. A Vue 3 + TypeScript + Vite + Pinia console
that projects the agent event log over a localhost HTTP + SSE API. Direction A,
"press box": a three-pane console (roster, transcript, detail rail) on warm
cream, with `prefers-color-scheme` dark support.

## Develop

```sh
cd web
npm install
npm run dev          # vite dev server on :5173, proxies /api to 127.0.0.1:7777
```

The dev server has no backend of its own. Two ways to see a working console:

1. Run the real `carlos web` server on `127.0.0.1:7777` and open the dev URL with
   the token in the fragment, e.g. `http://localhost:5173/#token=<token>`.
2. Use the built-in mock: open `http://localhost:5173/?mock` (or set `VITE_MOCK=1`).
   The mock patches `fetch` and `EventSource` with the sample data from the
   locked mockup: a streaming thread, a thread blocked on an approval, a
   foreign-owned (TUI) thread, and grouped threads. It also seeds a dev token
   into the URL fragment so the handshake path runs end to end.

## Build

```sh
npm run build        # vue-tsc typecheck + vite build
```

Output goes to `../internal/web/dist`, which the Go server embeds via `go:embed`.
The `outDir` is set in `vite.config.ts`; do not change it without updating the
embed path.

## Test

```sh
npm run test         # vitest run (jsdom)
```

Unit coverage targets the tricky store and wire logic: transcript insert with
out-of-order and duplicate seqs, delta seal/reset, approval resolve-after-expire,
the collapsed-set localStorage round trip, the token handshake, the client error
envelope and 409 typing, and the tool_call/tool_result render folding.

## Token handshake

The bearer token never touches localStorage (spec D9). On boot the connection
store reads `#token=...` from `location.hash`, hands it to the API client, then
scrubs the fragment with `history.replaceState` (no reload, no history entry).
From there the token rides as `Authorization: Bearer <token>` on every `/api`
fetch and as `?token=<token>` on the EventSource stream (EventSource cannot set
headers). It lives in memory only, for the lifetime of the tab.

## Architecture

- `src/api/`: `types.ts` (frozen wire contract), `client.ts` (fetch wrapper,
  bearer injection, error envelope, 409 typing), `sse.ts` (EventSource lifecycle),
  `render.ts` (event stream to render rows), `mock.ts` (dev-only fake backend).
- `src/stores/`: Pinia stores for `connection`, `threads` (roster poll,
  attach/detach), `groups` (CRUD + collapsed set), `transcript` (events by seq +
  delta buffer), `approvals` (pending by request_id), `toast`.
- `src/components/`: the component inventory from the plan: `TopBar`, `Roster`
  (`RosterHeader`, `GroupSection`, `ThreadRow`), `Stage` (`StageHeader`,
  `GuardBanner`, `TranscriptFeed` with `UserMessage` / `AssistantMessage` /
  `ToolCard` / `EventLine` / `StreamBlock`, `ApprovalBanner`, `Composer`,
  `EmptyState`), `Rail` (`ThreadMetaCard`, `ApprovalCard`, `CrewList`), `Toast`.

## Brand rules

Caveat handwriting is used in exactly two places: the topbar wordmark and the
empty-state headline. Everything else is Inter. No em-dashes in UI copy. No
left-edge colored stripes on cards: category lives in the corner-tag pattern.
Component CSS reads semantic tokens from `tokens.css`, never raw hex, so dark
mode flips entirely at the token layer.
