import { Box, Text, createCliRenderer } from "@opentui/core"
import { existsSync, readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

type Dict = Record<string, unknown>

type IssueRecord = {
  issue?: string
  issue_identifier?: string
  key?: string
  title?: string
  status?: string
  am_status?: string
  lane_role_hint?: string
  age?: string
  attention?: string
  updated_at?: string
  source?: string
  review?: string
  pr_url?: string
  outcome?: string
  linear_state?: string
  attempt?: number
  workspace?: string
  branch?: string
  next_action?: unknown
  blocker_reason?: unknown
  current_activity?: unknown
  external_state?: unknown
  agent_evidence_summary?: unknown
  recent_events?: unknown
  priority_bucket?: string
}

type SurfaceSnapshot = {
  schema_version: number
  observed_at: string
  config_path: string
  project_slug: string
  workspace_root: string
  source_precedence: string[]
  sqlite: {
    ok: boolean
    exists: boolean
    schema_version: number
    journal_mode: string
    busy_timeout_ms: number
    counts: Record<string, number>
    error?: string
  }
  issues: IssueRecord[]
  work_items?: IssueRecord[]
  issue_queue?: IssueRecord[]
  active_locks: Array<{
    issue: string
    workspace: string
    active: boolean
    stale: boolean
    owner: string
    renewed_at?: string
  }>
  active_lanes: Array<{
    name: string
    process_id: string
    cycle_number: number
    last_success_at?: string
    last_error?: string
    recovery_required: boolean
    active_task_key?: string
    active_task_role?: string
    active_lease_name?: string
    active_task_started_at?: string
    updated_at?: string
  }>
  worker_tasks: Array<{
    task_key: string
    role: string
    issue_key?: string
    attempt?: number
    status: string
    priority: number
    lease_name?: string
    available_at?: string
    updated_at?: string
  }>
  worker_results: Array<{
    task_key: string
    role: string
    lane_name?: string
    issue_key?: string
    attempt?: number
    status: string
    did_work: boolean
    reason?: string
    error?: string
    started_at?: string
    finished_at?: string
    updated_at?: string
  }>
  recent_events: Array<{
    sequence: number
    issue_key?: string
    source: string
    type: string
    occurred_at?: string
  }>
}

type IssueRow = {
  key: string
  title: string
  status: string
  lane: string
  age: string
  attention: string
  issue: IssueRecord
}

const moduleRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..")
const runnerRoot = resolveRunnerRoot()
const appNodeID = "agent-machine-tui"
const configuredPath = readFlag("--config")
const configPath = configuredPath ? resolve(process.cwd(), configuredPath) : resolve(runnerRoot ?? process.cwd(), "am.yaml")

let selectedIssueKey: string | undefined
let selectedIndex = 0
let lastSnapshot: SurfaceSnapshot | undefined
let lastMessage = ""
let refreshInFlight = false
let refreshQueued = false

const renderer = await createCliRenderer({
  exitOnCtrlC: true,
  targetFps: 15,
})

renderer.addInputHandler((sequence: string) => {
  switch (sequence) {
    case "q":
      renderer.destroy()
      process.exit(0)
    case "r":
      void requestRefresh()
      return true
    case "j":
    case "\x1B[B":
      moveRow(1)
      return true
    case "k":
    case "\x1B[A":
      moveRow(-1)
      return true
    default:
      return false
  }
})

await requestRefresh()

async function requestRefresh() {
  if (refreshInFlight) {
    refreshQueued = true
    return
  }

  refreshInFlight = true
  try {
    do {
      refreshQueued = false
      await refresh()
    } while (refreshQueued)
  } finally {
    refreshInFlight = false
  }
}

async function refresh() {
  render(lastSnapshot, "refreshing...")
  try {
    lastSnapshot = await loadSurfaceSnapshot()
    lastMessage = ""
  } catch (error) {
    lastMessage = error instanceof Error ? error.message : String(error)
  }
  clampSelection()
  render(lastSnapshot, lastMessage)
}

function moveRow(delta: number) {
  const rows = lastSnapshot ? issueRows(lastSnapshot) : []
  if (rows.length === 0) {
    selectedIndex = 0
    selectedIssueKey = undefined
    render(lastSnapshot, lastMessage)
    return
  }
  selectedIndex = Math.max(0, Math.min(rows.length - 1, selectedIndex + delta))
  selectedIssueKey = rows[selectedIndex]?.key
  render(lastSnapshot, lastMessage)
}

function clampSelection() {
  const rows = lastSnapshot ? issueRows(lastSnapshot) : []
  if (rows.length === 0) {
    selectedIndex = 0
    selectedIssueKey = undefined
    return
  }
  if (selectedIssueKey) {
    const refreshedIndex = rows.findIndex((row) => row.key === selectedIssueKey)
    if (refreshedIndex >= 0) {
      selectedIndex = refreshedIndex
      return
    }
  }
  selectedIndex = Math.max(0, Math.min(rows.length - 1, selectedIndex))
  selectedIssueKey = rows[selectedIndex]?.key
}

function render(snapshot?: SurfaceSnapshot, message = "") {
  try {
    renderer.root.remove(appNodeID)
  } catch {
    // The first render has no previous root node.
  }

  renderer.root.add(
    Box(
      {
        id: appNodeID,
        width: "100%",
        height: "100%",
        flexDirection: "column",
        backgroundColor: "#0f1419",
        padding: 1,
        gap: 1,
      },
      header(snapshot, message),
      summary(snapshot),
      body(snapshot, message),
      footer(),
    ),
  )
}

function header(snapshot?: SurfaceSnapshot, message = "") {
  const health = snapshot?.sqlite.ok ? "healthy" : snapshot?.sqlite.exists ? "degraded" : "missing"
  const color = snapshot?.sqlite.ok ? "#38d996" : "#e6b450"
  const right = message && message !== "refreshing..." ? compact(message, 34) : formatTime(snapshot?.observed_at) || "loading"
  return Box(
    { borderStyle: "single", borderColor: color, padding: 1, flexDirection: "row", gap: 2 },
    Text({ content: "Agent Machine", fg: "#f4f7fb" }),
    Text({ content: compact(`project ${snapshot?.project_slug ?? "..."}`, 22), fg: "#8ab4f8" }),
    Text({ content: `sqlite ${health}`, fg: color }),
    Text({ content: right, fg: message ? "#e6b450" : "#8d99a6" }),
  )
}

function summary(snapshot?: SurfaceSnapshot) {
  const issues = snapshot ? issueRecords(snapshot).length : 0
  const lanes = snapshot?.active_lanes.length ?? 0
  const tasks = snapshot?.worker_tasks.length ?? 0
  const blocked = snapshot?.worker_tasks.filter((task) => task.status === "reconciliation_needed").length ?? 0
  const locks = snapshot?.active_locks.filter((lock) => lock.active).length ?? 0
  const content = `Issues ${issues}    Active locks ${locks}    Lanes ${lanes}    Tasks ${tasks}    Reconcile ${blocked}`
  return Text({ content: fit(content, contentWidth()).padEnd(contentWidth()), fg: blocked > 0 ? "#ff6b6b" : "#d0d7de" })
}

function body(snapshot?: SurfaceSnapshot, message = "") {
  const width = contentWidth()
  if (!snapshot) {
    return panel("Issue Queue", [message || "Reading local surface snapshot..."], "#8ab4f8", Math.min(width, 44))
  }

  const rows = issueRows(snapshot)
  const selected = rows[selectedIndex]
  if (width < 82) {
    return Box(
      { flexDirection: "column", gap: 1, flexGrow: 1 },
      panel("Prioritized Issues", queueLines(rows), "#8ab4f8", width),
      panel("Selected Issue Evidence", selected ? evidenceLines(snapshot, selected) : ["No issues in snapshot."], "#c3e88d", width),
    )
  }
  const leftWidth = Math.max(44, Math.floor(width * 0.58))
  const rightWidth = Math.max(32, width - leftWidth - 1)
  return Box(
    { flexDirection: "row", gap: 1, flexGrow: 1 },
    panel("Prioritized Issues", queueLines(rows), "#8ab4f8", leftWidth),
    panel("Selected Issue Evidence", selected ? evidenceLines(snapshot, selected) : ["No issues in snapshot."], "#c3e88d", rightWidth),
  )
}

function panel(title: string, lines: string[], color: string, width: number) {
  const maxLines = Math.max(8, (process.stdout.rows ?? 28) - 9)
  return Box(
    { borderStyle: "single", borderColor: color, padding: 1, flexDirection: "column", gap: 0, width },
    Text({ content: fit(title, width - 4).padEnd(width - 4), fg: color }),
    ...lines.slice(0, maxLines).map((line) => Text({ content: fit(line, width - 4).padEnd(width - 4), fg: line.startsWith(">") ? "#f4f7fb" : line.endsWith(":") ? "#8ab4f8" : "#aeb6bf" })),
  )
}

function footer() {
  const width = contentWidth()
  return Text({
    content: fit("j/k or up/down select   r refresh   q quit   read-only surface", width).padEnd(width),
    fg: "#8d99a6",
  })
}

function issueRows(snapshot: SurfaceSnapshot): IssueRow[] {
  const records = issueRecords(snapshot)
  const hasIssueProjection = usesIssueProjection(snapshot)
  return records.map((issue) => {
    const key = issueKey(issue)
    const task = hasIssueProjection ? undefined : snapshot.worker_tasks.find((candidate) => candidate.issue_key === key)
    const lock = hasIssueProjection ? undefined : snapshot.active_locks.find((candidate) => candidate.issue === key)
    return {
      key,
      title: clean(issue.title) || "n/a",
      status: clean(issue.am_status) || clean(issue.status) || "n/a",
      lane: clean(issue.lane_role_hint) || (!hasIssueProjection ? clean(task?.role) : "") || "n/a",
      age: clean(issue.age) || (!hasIssueProjection ? relativeAge(issue.updated_at || task?.updated_at || lock?.renewed_at, snapshot.observed_at) : "") || "n/a",
      attention: clean(issue.attention) || "n/a",
      issue,
    }
  })
}

function issueRecords(snapshot: SurfaceSnapshot): IssueRecord[] {
  return snapshot.issue_queue ?? snapshot.work_items ?? snapshot.issues
}

function usesIssueProjection(snapshot: SurfaceSnapshot) {
  return snapshot.issue_queue !== undefined || snapshot.work_items !== undefined
}

function queueLines(rows: IssueRow[]) {
  if (rows.length === 0) {
    return ["No issues in snapshot."]
  }
  return rows.map((row, index) => {
    const marker = index === selectedIndex ? ">" : " "
    const title = row.title === "n/a" ? "" : ` ${compact(row.title, 18)}`
    return `${marker} ${compact(row.key, 9).padEnd(9)} ${compact(row.status, 16).padEnd(16)} ${compact(row.lane, 13).padEnd(13)} ${compact(row.age, 6).padStart(6)} ${compact(row.attention, 12)}${title}`
  })
}

function evidenceLines(snapshot: SurfaceSnapshot, row: IssueRow) {
  const issue = row.issue
  const key = row.key
  const hasIssueProjection = usesIssueProjection(snapshot)
  const task = hasIssueProjection ? undefined : snapshot.worker_tasks.find((candidate) => candidate.issue_key === key)
  const result = hasIssueProjection ? undefined : snapshot.worker_results.find((candidate) => candidate.issue_key === key)
  const lock = hasIssueProjection ? undefined : snapshot.active_locks.find((candidate) => candidate.issue === key)
  const lane = task ? snapshot.active_lanes.find((candidate) => candidate.active_task_key === task.task_key) : undefined
  const events = issueEvents(snapshot, issue)
  const activity = recordValue(issue.current_activity)
  const external = recordValue(issue.external_state)
  const evidence = recordValue(issue.agent_evidence_summary)

  return [
    `Header: ${key} ${row.status}  title: ${row.title}  source: ${clean(issue.source) || "n/a"}`,
    `Next: ${compactValueLine(issue.next_action, "next action", "n/a").replace(/^next action: /, "")}`,
    `Why: ${compactValueLine(issue.blocker_reason, "reason", row.attention === "none" || row.attention === "n/a" ? "none" : row.attention).replace(/^reason: /, "")}`,
    `Current Activity: lane ${field(activity, "lane") || clean(lane?.name) || row.lane}  task ${field(activity, "task") || clean(task?.task_key) || "n/a"}  lease ${field(activity, "lease") || clean(task?.lease_name) || clean(lock?.owner) || clean(lane?.active_lease_name) || "n/a"}  attempt ${field(activity, "attempt") || numberValue(issue.attempt) || numberValue(task?.attempt) || "n/a"}`,
    `Workspace/Branch/Timing: ${field(activity, "workspace") || clean(issue.workspace) || clean(lock?.workspace) || "n/a"}  ${field(activity, "branch") || clean(issue.branch) || "n/a"}  ${field(activity, "timing") || formatTime(issue.updated_at || task?.updated_at || lock?.renewed_at) || "n/a"}`,
    `External State: linear ${field(external, "linear") || clean(issue.linear_state) || "n/a"}  pr ${field(external, "pr") || clean(issue.pr_url) || "n/a"}  review ${field(external, "review") || clean(issue.review) || "n/a"}  checks ${field(external, "checks") || "n/a"}  merge ${field(external, "merge") || "n/a"}`,
    `Agent Output/Evidence: ${compactValueLine(issue.agent_evidence_summary, "evidence", "none").replace(/^evidence: /, "")}`,
    `Outcome/Worker/Error: ${clean(issue.outcome) || field(evidence, "outcome") || "n/a"}  ${workerEvidence(evidence, result)}  ${field(evidence, "worker_error") || result?.error || "none"}`,
    "Recent Events:",
    ...(events.length > 0 ? events.slice(0, 3).map((event) => `${formatTime(event.occurred_at) || "n/a"} ${event.type} ${event.source}`) : ["none"]),
  ]
}

function workerEvidence(evidence: Dict | undefined, result: SurfaceSnapshot["worker_results"][number] | undefined) {
  const status = field(evidence, "worker_status") || clean(result?.status)
  const reason = field(evidence, "worker_reason") || clean(result?.reason)
  if (!status) {
    return "n/a"
  }
  return reason ? `${status} (${reason})` : status
}

function contentWidth() {
  return Math.max(36, (process.stdout.columns ?? 80) - 4)
}

function issueEvents(snapshot: SurfaceSnapshot, issue: IssueRecord) {
  const embedded = normalizeEvents(issue.recent_events)
  if (embedded.length > 0) {
    return embedded
  }
  const key = issueKey(issue)
  return snapshot.recent_events.filter((event) => event.issue_key === key)
}

function normalizeEvents(value: unknown): SurfaceSnapshot["recent_events"] {
  if (!Array.isArray(value)) {
    return []
  }
  return value.flatMap((candidate) => {
    if (!candidate || typeof candidate !== "object") {
      return []
    }
    const event = candidate as Dict
    return [{
      sequence: typeof event.sequence === "number" ? event.sequence : 0,
      issue_key: stringFrom(event.issue_key),
      source: stringFrom(event.source) || "n/a",
      type: stringFrom(event.type) || stringFrom(event.event_type) || "n/a",
      occurred_at: stringFrom(event.occurred_at) || stringFrom(event.timestamp),
    }]
  })
}

function valueLines(value: unknown, label: string, fallback: string): string[] {
  if (typeof value === "string" && value.trim()) {
    return [`${label}: ${value.trim()}`]
  }
  if (Array.isArray(value) && value.length > 0) {
    return value.map((entry) => `- ${stringifyValue(entry)}`)
  }
  if (value && typeof value === "object") {
    const entries = Object.entries(value as Dict).filter(([, entry]) => entry !== undefined && entry !== null && entry !== "")
    if (entries.length > 0) {
      return entries.map(([key, entry]) => `${key}: ${stringifyValue(entry)}`)
    }
  }
  return [`${label}: ${fallback}`]
}

function compactValueLine(value: unknown, label: string, fallback: string): string {
  const lines = valueLines(value, label, fallback)
  if (lines.length === 0) {
    return `${label}: ${fallback}`
  }
  return lines.map((line) => line.replace(/^- /, "")).join(" | ")
}

function recordValue(value: unknown): Dict | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Dict : undefined
}

function field(record: Dict | undefined, key: string) {
  if (!record) {
    return ""
  }
  return stringifyValue(record[key]).replace(/^n\/a$/, "")
}

async function loadSurfaceSnapshot(): Promise<SurfaceSnapshot> {
  const command = process.env.AM_BIN?.trim()
  const cwd = command ? runnerRoot ?? process.cwd() : requireRunnerRoot()
  const args = command
    ? [command, "surface", "snapshot", "--config", configPath]
    : ["go", "run", ".", "surface", "snapshot", "--config", configPath]
  const proc = Bun.spawn(args, {
    cwd,
    env: {
      ...process.env,
      GOCACHE: process.env.GOCACHE ?? "/tmp/agent-machine-go-cache",
    },
    stdout: "pipe",
    stderr: "pipe",
  })
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ])
  if (exitCode !== 0) {
    throw new Error((stderr || stdout || `surface snapshot exited ${exitCode}`).trim())
  }
  return parseSurfaceSnapshot(stdout)
}

function parseSurfaceSnapshot(stdout: string): SurfaceSnapshot {
  const value = JSON.parse(stdout) as unknown
  assertSurfaceSnapshot(value)
  return value
}

function assertSurfaceSnapshot(value: unknown): asserts value is SurfaceSnapshot {
  const snapshot = requireRecord(value, "surface snapshot")
  if (snapshot.schema_version !== 1) {
    throw new Error(`unsupported surface snapshot schema_version ${String(snapshot.schema_version)}`)
  }
  requireString(snapshot.observed_at, "observed_at")
  requireString(snapshot.config_path, "config_path")
  requireString(snapshot.project_slug, "project_slug")
  requireString(snapshot.workspace_root, "workspace_root")
  requireArray(snapshot.source_precedence, "source_precedence")
  const sqlite = requireRecord(snapshot.sqlite, "sqlite")
  if (typeof sqlite.ok !== "boolean" || typeof sqlite.exists !== "boolean") {
    throw new Error("surface snapshot sqlite health must include boolean ok and exists")
  }
  requireNumber(sqlite.schema_version, "sqlite.schema_version")
  requireString(sqlite.journal_mode, "sqlite.journal_mode")
  requireNumber(sqlite.busy_timeout_ms, "sqlite.busy_timeout_ms")
  requireRecord(sqlite.counts, "sqlite.counts")
  for (const key of ["issues", "active_locks", "active_lanes", "worker_tasks", "worker_results", "recent_events"]) {
    requireArray(snapshot[key], key)
  }
  if (snapshot.work_items !== undefined) {
    requireArray(snapshot.work_items, "work_items")
  }
  if (snapshot.issue_queue !== undefined) {
    requireArray(snapshot.issue_queue, "issue_queue")
  }
}

function readFlag(name: string) {
  const index = process.argv.indexOf(name)
  if (index < 0) {
    return undefined
  }
  return process.argv[index + 1]
}

function issueKey(issue: IssueRecord) {
  return clean(issue.issue_identifier) || clean(issue.issue) || clean(issue.key) || "n/a"
}

function formatTime(value?: string) {
  if (!value || value.startsWith("0001-01-01")) {
    return ""
  }
  return value.replace("T", " ").replace(/\.\d+Z$/, "Z")
}

function relativeAge(value: string | undefined, observedAt: string) {
  if (!value || value.startsWith("0001-01-01")) {
    return ""
  }
  const then = Date.parse(value)
  const now = Date.parse(observedAt)
  if (!Number.isFinite(then) || !Number.isFinite(now) || now < then) {
    return ""
  }
  const minutes = Math.floor((now - then) / 60000)
  if (minutes < 60) {
    return `${minutes}m`
  }
  const hours = Math.floor(minutes / 60)
  if (hours < 48) {
    return `${hours}h`
  }
  return `${Math.floor(hours / 24)}d`
}

function compact(value: string, max: number) {
  return fit(value, max).trimEnd()
}

function fit(value: string, max: number) {
  if (value.length <= max) {
    return value
  }
  return `${value.slice(0, Math.max(0, max - 3))}...`
}

function clean(value: unknown) {
  return typeof value === "string" ? value.trim() : ""
}

function stringFrom(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim() : undefined
}

function numberValue(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? String(value) : ""
}

function stringifyValue(value: unknown): string {
  if (typeof value === "string") {
    return value.trim() || "n/a"
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value)
  }
  if (Array.isArray(value)) {
    return value.map(stringifyValue).join(", ") || "n/a"
  }
  if (value && typeof value === "object") {
    return Object.entries(value as Dict)
      .filter(([, entry]) => entry !== undefined && entry !== null && entry !== "")
      .map(([key, entry]) => `${key}=${stringifyValue(entry)}`)
      .join(" ") || "n/a"
  }
  return "n/a"
}

function resolveRunnerRoot() {
  const explicit = process.env.AM_ROOT?.trim()
  if (explicit) {
    return resolve(process.cwd(), explicit)
  }
  return findRunnerRoot([process.cwd(), moduleRoot])
}

function requireRunnerRoot() {
  if (runnerRoot) {
    return runnerRoot
  }
  throw new Error("could not find agent-machine runner root; run from the repo or set AM_BIN")
}

function findRunnerRoot(candidates: string[]) {
  for (const candidate of candidates) {
    let dir = resolve(candidate)
    for (;;) {
      if (isRunnerRoot(dir)) {
        return dir
      }
      const parent = dirname(dir)
      if (parent === dir) {
        break
      }
      dir = parent
    }
  }
  return undefined
}

function isRunnerRoot(dir: string) {
  const mod = resolve(dir, "go.mod")
  if (!existsSync(mod)) {
    return false
  }
  try {
    return readFileSync(mod, "utf8").includes("module github.com/weskor/agent-machine")
  } catch {
    return false
  }
}

function requireRecord(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`surface snapshot ${label} must be an object`)
  }
  return value as Record<string, unknown>
}

function requireArray(value: unknown, label: string) {
  if (!Array.isArray(value)) {
    throw new Error(`surface snapshot ${label} must be an array`)
  }
}

function requireString(value: unknown, label: string) {
  if (typeof value !== "string") {
    throw new Error(`surface snapshot ${label} must be a string`)
  }
}

function requireNumber(value: unknown, label: string) {
  if (typeof value !== "number") {
    throw new Error(`surface snapshot ${label} must be a number`)
  }
}
