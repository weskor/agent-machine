import { Box, Text, createCliRenderer } from "@opentui/core"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

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
  issues: Array<{
    issue: string
    status: string
    review?: string
    pr_url?: string
    outcome?: string
    source: string
    updated_at?: string
  }>
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

type SectionID = "overview" | "issues" | "lanes" | "tasks" | "events"

type RowItem = {
  label: string
  value: string
  tone: "normal" | "good" | "warn" | "bad" | "muted"
  details: string[]
}

const sections: Array<{ id: SectionID; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "issues", label: "Issues" },
  { id: "lanes", label: "Lanes" },
  { id: "tasks", label: "Tasks" },
  { id: "events", label: "Events" },
]

const sourceRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..")
const appNodeID = "pi-symphony-tui"
const configuredPath = readFlag("--config")
const configPath = configuredPath ? resolve(process.cwd(), configuredPath) : resolve(sourceRoot, "symphony.yaml")
const selectedBySection = new Map<SectionID, number>()
let currentSection = 0
let lastSnapshot: SurfaceSnapshot | undefined
let lastMessage = ""

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
      void refresh()
      return true
    case "\t":
    case "l":
    case "\x1B[C":
      moveSection(1)
      return true
    case "h":
    case "\x1B[D":
      moveSection(-1)
      return true
    case "j":
    case "\x1B[B":
      moveRow(1)
      return true
    case "k":
    case "\x1B[A":
      moveRow(-1)
      return true
    case "1":
    case "2":
    case "3":
    case "4":
    case "5":
      currentSection = Number(sequence) - 1
      clampSelection()
      render(lastSnapshot, lastMessage)
      return true
    default:
      return false
  }
})

await refresh()

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

function moveSection(delta: number) {
  currentSection = (currentSection + delta + sections.length) % sections.length
  clampSelection()
  render(lastSnapshot, lastMessage)
}

function moveRow(delta: number) {
  const section = activeSection()
  const rows = lastSnapshot ? rowsForSection(lastSnapshot, section) : []
  if (rows.length === 0) {
    selectedBySection.set(section, 0)
    render(lastSnapshot, lastMessage)
    return
  }
  const current = selectedBySection.get(section) ?? 0
  selectedBySection.set(section, Math.max(0, Math.min(rows.length - 1, current + delta)))
  render(lastSnapshot, lastMessage)
}

function clampSelection() {
  const section = activeSection()
  const rows = lastSnapshot ? rowsForSection(lastSnapshot, section) : []
  const current = selectedBySection.get(section) ?? 0
  selectedBySection.set(section, Math.max(0, Math.min(Math.max(0, rows.length - 1), current)))
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
  const right = message && message !== "refreshing..." ? compact(message, 28) : formatTime(snapshot?.observed_at) || "loading"
  return Box(
    { borderStyle: "single", borderColor: color, padding: 1, flexDirection: "row", gap: 3 },
    Text({ content: "Pi Symphony", fg: "#f4f7fb" }),
    Text({ content: compact(`project ${snapshot?.project_slug ?? "..."}`, 22), fg: "#8ab4f8" }),
    Text({ content: `sqlite ${health}`, fg: color }),
    Text({ content: right, fg: message ? "#e6b450" : "#8d99a6" }),
  )
}

function summary(snapshot?: SurfaceSnapshot) {
  const issues = snapshot?.issues.length ?? 0
  const lanes = snapshot?.active_lanes.length ?? 0
  const tasks = snapshot?.worker_tasks.length ?? 0
  const blocked = snapshot?.worker_tasks.filter((task) => task.status === "reconciliation_needed").length ?? 0
  const locks = snapshot?.active_locks.filter((lock) => lock.active).length ?? 0
  const content = `Issues ${issues}    Active locks ${locks}    Lanes ${lanes}    Tasks ${tasks}    Reconcile ${blocked}`
  return Text({ content: fit(content, 76).padEnd(76), fg: blocked > 0 ? "#ff6b6b" : "#d0d7de" })
}

function body(snapshot?: SurfaceSnapshot, message = "") {
  if (!snapshot) {
    return panel("Loading", [message || "Reading local surface snapshot..."], "#8ab4f8", 76)
  }

  const section = activeSection()
  const rows = rowsForSection(snapshot, section)
  const selected = selectedBySection.get(section) ?? 0
  const item = rows[selected]

  return Box(
    { flexDirection: "row", gap: 1, flexGrow: 1 },
    panel("Views", navLines(), "#d0d7de", 16),
    panel(sections[currentSection].label, listLines(rows, selected), sectionColor(section), 34),
    panel("Details", item ? item.details : ["No rows in this view."], "#c3e88d", 24),
  )
}

function panel(title: string, lines: string[], color: string, width: number) {
  return Box(
    { borderStyle: "single", borderColor: color, padding: 1, flexDirection: "column", gap: 0, width },
    Text({ content: fit(title, width - 4).padEnd(width - 4), fg: color }),
    ...lines.slice(0, 14).map((line) => Text({ content: fit(line, width - 4).padEnd(width - 4), fg: line.startsWith(">") ? "#f4f7fb" : "#aeb6bf" })),
  )
}

function footer() {
  return Text({
    content: fit("tab/h/l view   j/k/up/down row   1-5 jump   r refresh   q quit   read-only surface", 76).padEnd(76),
    fg: "#8d99a6",
  })
}

function navLines() {
  return sections.map((section, index) => {
    const marker = index === currentSection ? ">" : " "
    return `${marker} ${index + 1} ${section.label}`
  })
}

function listLines(rows: RowItem[], selected: number) {
  if (rows.length === 0) {
    return ["No rows."]
  }
  return rows.map((row, index) => {
    const marker = index === selected ? ">" : " "
    return `${marker} ${compact(row.label, 12).padEnd(12)} ${compact(row.value, 16)}`
  })
}

function rowsForSection(snapshot: SurfaceSnapshot, section: SectionID): RowItem[] {
  switch (section) {
    case "overview":
      return overviewRows(snapshot)
    case "issues":
      return issueRows(snapshot)
    case "lanes":
      return laneRows(snapshot)
    case "tasks":
      return taskRows(snapshot)
    case "events":
      return eventRows(snapshot)
  }
}

function overviewRows(snapshot: SurfaceSnapshot): RowItem[] {
  const counts = snapshot.sqlite.counts
  return [
    {
      label: "SQLite",
      value: snapshot.sqlite.ok ? "healthy" : snapshot.sqlite.exists ? "degraded" : "missing",
      tone: snapshot.sqlite.ok ? "good" : "warn",
      details: [
        `schema: ${snapshot.sqlite.schema_version || "n/a"}`,
        `journal: ${snapshot.sqlite.journal_mode || "n/a"}`,
        `busy timeout: ${snapshot.sqlite.busy_timeout_ms || 0}ms`,
        snapshot.sqlite.error ? `error: ${snapshot.sqlite.error}` : "error: none",
      ],
    },
    {
      label: "Attempts",
      value: String(counts.issue_attempts ?? snapshot.issues.length),
      tone: "normal",
      details: [`issues in snapshot: ${snapshot.issues.length}`, `terminal outcomes: ${counts.terminal_outcomes ?? 0}`, `review states: ${counts.review_states ?? 0}`],
    },
    {
      label: "Workers",
      value: `${snapshot.worker_tasks.length} tasks`,
      tone: "normal",
      details: [`tasks: ${snapshot.worker_tasks.length}`, `results: ${snapshot.worker_results.length}`, `payload refs: ${counts.worker_payload_refs ?? 0}`],
    },
    {
      label: "Precedence",
      value: snapshot.source_precedence[0] ?? "n/a",
      tone: "muted",
      details: snapshot.source_precedence.map((source, index) => `${index + 1}. ${source}`),
    },
  ]
}

function issueRows(snapshot: SurfaceSnapshot): RowItem[] {
  if (snapshot.issues.length === 0) {
    return []
  }
  return snapshot.issues.map((issue) => ({
    label: issue.issue,
    value: issue.status,
    tone: issue.status.includes("failed") ? "bad" : issue.status === "success" ? "good" : "normal",
    details: [
      `issue: ${issue.issue}`,
      `status: ${issue.status || "n/a"}`,
      `source: ${issue.source || "n/a"}`,
      `review: ${issue.review || "n/a"}`,
      `outcome: ${issue.outcome || "n/a"}`,
      `updated: ${formatTime(issue.updated_at) || "n/a"}`,
      `pr: ${issue.pr_url || "n/a"}`,
    ],
  }))
}

function laneRows(snapshot: SurfaceSnapshot): RowItem[] {
  return snapshot.active_lanes.map((lane) => ({
    label: lane.name,
    value: lane.recovery_required ? "recovery" : `cycle ${lane.cycle_number}`,
    tone: lane.recovery_required || lane.last_error ? "warn" : "good",
    details: [
      `lane: ${lane.name}`,
      `cycle: ${lane.cycle_number}`,
      `process: ${lane.process_id || "n/a"}`,
      `active task: ${lane.active_task_key || "n/a"}`,
      `active role: ${lane.active_task_role || "n/a"}`,
      `lease: ${lane.active_lease_name || "n/a"}`,
      `updated: ${formatTime(lane.updated_at) || "n/a"}`,
      `last error: ${lane.last_error || "none"}`,
    ],
  }))
}

function taskRows(snapshot: SurfaceSnapshot): RowItem[] {
  return snapshot.worker_tasks.map((task) => ({
    label: task.role,
    value: task.status,
    tone: task.status === "reconciliation_needed" ? "bad" : task.status === "failed" ? "warn" : "normal",
    details: [
      `task: ${task.task_key}`,
      `role: ${task.role}`,
      `issue: ${task.issue_key || "n/a"}`,
      `status: ${task.status}`,
      `priority: ${task.priority}`,
      `lease: ${task.lease_name || "n/a"}`,
      `available: ${formatTime(task.available_at) || "n/a"}`,
      `updated: ${formatTime(task.updated_at) || "n/a"}`,
    ],
  }))
}

function eventRows(snapshot: SurfaceSnapshot): RowItem[] {
  return snapshot.recent_events.map((event) => ({
    label: `#${event.sequence}`,
    value: event.type,
    tone: event.type.includes("failed") || event.type.includes("reconciliation") ? "warn" : "muted",
    details: [
      `sequence: ${event.sequence}`,
      `type: ${event.type}`,
      `source: ${event.source}`,
      `issue: ${event.issue_key || "n/a"}`,
      `at: ${formatTime(event.occurred_at) || "n/a"}`,
    ],
  }))
}

function activeSection(): SectionID {
  return sections[currentSection].id
}

function sectionColor(section: SectionID) {
  switch (section) {
    case "overview":
      return "#8ab4f8"
    case "issues":
      return "#a78bfa"
    case "lanes":
      return "#38d996"
    case "tasks":
      return "#e6b450"
    case "events":
      return "#d0d7de"
  }
}

async function loadSurfaceSnapshot(): Promise<SurfaceSnapshot> {
  const command = process.env.PI_SYMPHONY_BIN?.trim()
  const args = command
    ? [command, "surface", "snapshot", "--config", configPath]
    : ["go", "run", ".", "surface", "snapshot", "--config", configPath]
  const proc = Bun.spawn(args, {
    cwd: sourceRoot,
    env: {
      ...process.env,
      GOCACHE: process.env.GOCACHE ?? "/tmp/pi-symphony-go-cache",
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
  return JSON.parse(stdout) as SurfaceSnapshot
}

function readFlag(name: string) {
  const index = process.argv.indexOf(name)
  if (index < 0) {
    return undefined
  }
  return process.argv[index + 1]
}

function formatTime(value?: string) {
  if (!value || value.startsWith("0001-01-01")) {
    return ""
  }
  return value.replace("T", " ").replace(/\.\d+Z$/, "Z")
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
