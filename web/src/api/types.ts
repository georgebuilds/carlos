// carlos web · wire contract types (mirror of web-spec §8, frozen).
// Unknown-field tolerant: never assert exhaustiveness on wire payloads.

// ── wire states (underscore form), spec §8.1 ──────────────────────────
export type WireState =
  | 'running'
  | 'queued'
  | 'spawning'
  | 'awaiting_input'
  | 'blocked'
  | 'paused'
  | 'compacting'
  | 'cancelling'
  | 'done'
  | 'failed'
  | 'orphaned'

// foreign-owned threads carry a synthetic overlay rendered as "in the TUI".
// it is not a wire state; the threads store derives it from ownership.
export type DisplayState = WireState | 'foreign'

// ── event kinds (WireEvent.data shapes) ───────────────────────────────
export interface UserMessageData { text: string }
export interface AssistantMessageData { text: string; error?: boolean }
export interface ToolCallData { name: string; input: unknown }
export interface ToolResultData {
  name: string
  output_preview: string
  is_error: boolean
  truncated: boolean
}
export interface StateData { state: WireState; detail?: string }
export interface ResearchPhaseData {
  phase: string
  done: boolean
  elapsed_ms: number
  err?: string
}
export interface DeltaData { text: string }
export interface ApprovalRequestData {
  request_id: string
  name: string
  input: unknown
  layer_reason?: string
}
export interface ApprovalResolvedData {
  request_id: string
  decision: ApprovalDecision
}
export interface ChildSnapshot {
  id: string
  state: WireState
  title: string
  last_tool: string
  tokens: number
  cost_cents: number
  started_at: string
}
export interface ChildrenData { children: ChildSnapshot[] }

// ── the envelope ──────────────────────────────────────────────────────
// seq is present on persisted kinds, absent on ephemeral kinds
// (delta, delta_reset, approval_request).
export interface WireEvent<T = Record<string, unknown>> {
  seq?: number
  thread: string
  ts: string
  kind: string
  data: T
}

export type ApprovalDecision = 'deny' | 'allow' | 'allow_always'

// ── repo reference (WB-1 overlay) ─────────────────────────────────────
// The git repository a thread's working directory resolves to. Absent when
// the thread has no resolvable repo (it lands in the "No repository"
// catch-all on the by-repo home). `root` is the absolute git root; `name`
// is its basename (the section header label).
export interface RepoRef {
  root: string
  name: string
}

// ── thread summary (spec §8.2, + additive group_id) ───────────────────
export interface BackendCaps {
  attach?: boolean
  spawn?: boolean
  approvals?: boolean
  [k: string]: unknown
}

export interface ThreadSummary {
  id: string
  title: string
  model: string
  state: WireState
  attached: boolean
  created_at: string
  updated_at: string
  preview: string
  user_msgs: number
  frame: string
  backend: string
  group_id?: string | null
  hidden?: boolean // web-local roster blacklist (folded out of the default view)
  // additive (WB-1): the git repo this thread's cwd resolves to. Absent when
  // the thread has no repo; the by-repo home folds those into "No repository".
  repo?: RepoRef
  capabilities: BackendCaps
  // foreign-owner overlay, surfaced by the server when another process owns it.
  owner?: 'tui' | 'web' | null
  heartbeat_age?: string
  // additive: set only for sub-agent rows. The server filters children out
  // of /api/threads, so a well-behaved wire never sends it; the roster
  // store drops any summary carrying one as defense in depth (sub-agents
  // belong to the crew column, never the left bar).
  parent_id?: string | null
}

// ── groups (plan §4.3) ────────────────────────────────────────────────
export interface Group {
  id: string
  name: string
  pos: number
  threads: number // member count
}

// ── meta (GET /api/meta) ──────────────────────────────────────────────
// An agent the roster's "+ new" menu can start a thread with: detected
// (installed) backends whose create capability is on.
export interface AgentInfo {
  name: string // backend id ("carlos", "cc")
  display: string // menu label ("carlos thread", "Claude Code")
  can_create: boolean
}

export interface Meta {
  version?: string
  addr?: string
  backends?: string[]
  agents?: AgentInfo[]
  [k: string]: unknown
}

// ── error envelope ────────────────────────────────────────────────────
export interface WireError {
  status: number
  code: string
  message: string
}
