const REFRESH_INTERVAL_MS = 5000;
const terminalPhases = new Set(["Completed", "Failed"]);

const state = {
  historyEnabled: null,
  isPaused: false,
  isRefreshing: false,
  lastAnnouncedSyncStatus: "",
  refreshTimer: null,
};

const byID = (id) => document.getElementById(id);

async function fetchJSON(path, options) {
  const response = await fetch(path, options);
  const contentType = response.headers.get("content-type") || "";
  const body = contentType.includes("application/json") ? await response.json() : {};
  if (!response.ok) {
    throw new Error(body.error || `Request failed (${response.status})`);
  }
  return body;
}

function escapeHTML(value) {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function emptyRow(columns, message, isError = false) {
  const className = isError ? "empty-row error-row" : "empty-row";
  return `<tr class="${className}"><td colspan="${columns}">${escapeHTML(message)}</td></tr>`;
}

function loadingRows(columns) {
  return Array.from({ length: 3 }, () =>
    `<tr class="skeleton-row"><td colspan="${columns}"><span class="skeleton"></span></td></tr>`,
  ).join("");
}

function statusBadge(phase) {
  const toneByPhase = {
    Pending: "info",
    Syncing: "warning",
    Checkpointing: "warning",
    Transferring: "warning",
    Restoring: "warning",
    Completed: "success",
    Failed: "danger",
  };
  const tone = toneByPhase[phase] || "muted";
  return `<span class="badge badge-${tone}">${escapeHTML(phase || "Pending")}</span>`;
}

function registeredBadge(migrating) {
  if (migrating) return '<span class="badge badge-warning">Migrating</span>';
  return '<span class="badge badge-success">Registered</span>';
}

function formatDuration(milliseconds) {
  if (milliseconds === null || milliseconds === undefined) return "—";
  if (milliseconds < 1000) return `${milliseconds} ms`;
  if (milliseconds < 60000) return `${(milliseconds / 1000).toFixed(1)} s`;
  const minutes = Math.floor(milliseconds / 60000);
  const seconds = Math.round((milliseconds % 60000) / 1000);
  return `${minutes}m ${seconds}s`;
}

function displayEndpoint(address, node) {
  if (node) return `${escapeHTML(node)}<span class="table-subtext">${escapeHTML(address || "No endpoint")}</span>`;
  return escapeHTML(address || "No endpoint");
}

function renderPodRow(pod) {
  const workload = pod.workload || "Unassigned";
  return `<tr>
    <td data-label="Pod">${escapeHTML(pod.name)}<span class="table-subtext">${escapeHTML(workload)}</span></td>
    <td data-label="Node / endpoint">${displayEndpoint(pod.address, pod.node)}</td>
    <td data-label="Mode">${pod.processMigration || pod.volumeMigration ? "Process + volume" : "Not configured"}</td>
    <td data-label="Status">${registeredBadge(pod.migrating)}</td>
  </tr>`;
}

function mechanisms(migration) {
  const values = [];
  if (migration.processMigration) values.push("Process");
  if (migration.volumeMigration) {
    const rounds = migration.syncRounds ? ` · ${migration.syncRound || 0}/${migration.syncRounds} sync` : "";
    values.push(`Volume${rounds}`);
  }
  return values.length ? escapeHTML(values.join(" + ")) : "Not configured";
}

function renderMigrationRow(migration) {
  const route = `${migration.sourceNode || "—"} → ${migration.targetNode || "—"}`;
  return `<tr>
    <td data-label="Migration">${escapeHTML(migration.name)}<span class="table-subtext">${escapeHTML(migration.workload)}</span></td>
    <td data-label="Route">${escapeHTML(route)}<span class="table-subtext">${escapeHTML(migration.sourcePod || migration.podName || "Pod pending")}</span></td>
    <td data-label="Mechanisms">${mechanisms(migration)}</td>
    <td data-label="Phase">${statusBadge(migration.phase)}</td>
  </tr>`;
}

function renderHistoryRow(record) {
  const steps = record.seeded
    ? '<span class="table-subtext">Recorded before the latest operator restart</span>'
    : (record.steps || []).map((step) =>
      `<span>${escapeHTML(step.phase)} <strong>${formatDuration(step.durationMs)}</strong></span>`,
    ).join('<span class="phase-arrow">→</span>') || '<span class="table-subtext">No timing data yet</span>';
  return `<tr>
    <td data-label="Migration">${escapeHTML(record.name)}<span class="table-subtext">${escapeHTML(record.workload || "—")}</span></td>
    <td data-label="Timing"><span class="phase-steps">${steps}</span></td>
    <td data-label="Total">${formatDuration(record.totalMs)}</td>
    <td data-label="Downtime">${formatDuration(record.downtimeMs)}</td>
    <td data-label="Outcome">${statusBadge(record.phase)}</td>
  </tr>`;
}

function setSyncStatus(mode, text, { announce = false, clearAnnouncement = false } = {}) {
  const status = byID("sync-status");
  status.className = `sync-status ${mode}`;
  byID("sync-status-text").textContent = text;
  if (announce && state.lastAnnouncedSyncStatus !== text) {
    byID("sync-announcer").textContent = text;
    state.lastAnnouncedSyncStatus = text;
  }
  if (clearAnnouncement) {
    byID("sync-announcer").textContent = "";
    state.lastAnnouncedSyncStatus = "";
  }
}

function setOverview(pods, migrations, history) {
  const registered = pods.filter((pod) => !pod.migrating).length;
  const activeMigrations = migrations.filter((migration) => !terminalPhases.has(migration.phase)).length;
  const summary = history?.summary;

  byID("metric-registered").textContent = registered;
  byID("metric-active").textContent = activeMigrations;
  byID("metric-success").textContent = summary ? `${Math.round((summary.successRate || 0) * 100)}%` : "—";
  byID("metric-downtime").textContent = summary ? formatDuration(summary.avgDowntimeMs) : "—";
  byID("overview-caption").innerHTML = activeMigrations
    ? `<strong>${activeMigrations}</strong> migration${activeMigrations === 1 ? "" : "s"} need attention`
    : "No active migrations";
}

function renderPods(result) {
  const target = byID("pods-body");
  if (result.status === "rejected") {
    target.innerHTML = emptyRow(4, `Could not load live pods: ${result.reason.message}`, true);
    return [];
  }
  const pods = result.value.pods || [];
  target.innerHTML = pods.length
    ? pods.map(renderPodRow).join("")
    : emptyRow(4, "No execution agents have registered yet.");

  byID("pod-names").innerHTML = pods.map((pod) => `<option value="${escapeHTML(pod.name)}"></option>`).join("");
  const workloads = [...new Set(pods.map((pod) => pod.workload).filter(Boolean))];
  byID("workload-names").innerHTML = workloads.map((workload) => `<option value="${escapeHTML(workload)}"></option>`).join("");
  return pods;
}

function renderMigrations(result) {
  const target = byID("migrations-body");
  if (result.status === "rejected") {
    target.innerHTML = emptyRow(4, `Could not load migrations: ${result.reason.message}`, true);
    return [];
  }
  const migrations = (result.value.migrations || []).slice().reverse();
  target.innerHTML = migrations.length
    ? migrations.map(renderMigrationRow).join("")
    : emptyRow(4, "No migrations have been requested.");
  return migrations;
}

function renderHistory(result) {
  const target = byID("history-body");
  const summaryTarget = byID("history-summary");
  const toggle = byID("history-toggle");

  if (result.status === "rejected") {
    state.historyEnabled = null;
    toggle.disabled = true;
    toggle.textContent = "Unavailable";
    target.innerHTML = emptyRow(5, `Could not load history: ${result.reason.message}`, true);
    summaryTarget.innerHTML = "";
    return null;
  }

  const history = result.value;
  state.historyEnabled = Boolean(history.enabled);
  toggle.disabled = false;
  toggle.textContent = state.historyEnabled ? "Pause collection" : "Resume collection";

  if (!state.historyEnabled) {
    summaryTarget.innerHTML = "";
    target.innerHTML = emptyRow(5, "History collection is paused. Resume it to record new migration timing.");
    return history;
  }

  const summary = history.summary || {};
  const chips = [
    ["Recorded", summary.total ?? 0],
    ["Completed", summary.completed ?? 0],
    ["Failed", summary.failed ?? 0],
    ["Average duration", formatDuration(summary.avgTotalMs)],
  ];
  summaryTarget.innerHTML = chips.map(([label, value]) =>
    `<span class="summary-chip">${label}<strong>${value}</strong></span>`,
  ).join("");

  const records = (history.migrations || []).slice().reverse();
  target.innerHTML = records.length
    ? records.map(renderHistoryRow).join("")
    : emptyRow(5, "No migrations have completed since collection was enabled.");
  return history;
}

async function refresh({ force = false } = {}) {
  if ((!force && state.isPaused) || state.isRefreshing) return;
  state.isRefreshing = true;
  setSyncStatus("is-syncing", "Refreshing live state");

  const results = await Promise.allSettled([
    fetchJSON("/api/v1/pods"),
    fetchJSON("/api/v1/migrations"),
    fetchJSON("/api/v1/history"),
  ]);

  const pods = renderPods(results[0]);
  const migrations = renderMigrations(results[1]);
  const history = renderHistory(results[2]);
  setOverview(pods, migrations, history);

  const hasFailure = results.some((result) => result.status === "rejected");
  setSyncStatus(
    hasFailure ? "has-error" : "",
    hasFailure ? "Some live data could not be loaded" : "Live data synchronized",
    { announce: hasFailure, clearAnnouncement: !hasFailure },
  );
  byID("last-refresh").textContent = `Last updated ${new Date().toLocaleTimeString()}`;
  state.isRefreshing = false;
}

function togglePolling() {
  state.isPaused = !state.isPaused;
  const button = byID("poll-toggle");
  button.textContent = state.isPaused ? "Resume live updates" : "Pause live updates";
  button.setAttribute("aria-pressed", String(state.isPaused));
  if (state.isPaused) {
    setSyncStatus("is-paused", "Live updates paused");
    return;
  }
  refresh({ force: true });
}

async function toggleHistory() {
  const toggle = byID("history-toggle");
  toggle.disabled = true;
  try {
    await fetchJSON("/api/v1/history/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: !state.historyEnabled }),
    });
    await refresh({ force: true });
  } catch (error) {
    byID("history-body").innerHTML = emptyRow(5, `Could not update collection: ${error.message}`, true);
  } finally {
    toggle.disabled = state.historyEnabled === null;
  }
}

function setFormStatus(message, mode = "") {
  const status = byID("migration-status");
  status.textContent = message;
  status.className = `form-status ${mode}`;
}

async function submitMigration(event) {
  event.preventDefault();
  const workload = byID("workload").value.trim();
  const podName = byID("pod-name").value.trim();
  const sourceNode = byID("origin-node").value.trim();
  const targetNode = byID("dest-node").value.trim();
  const button = byID("migrate-button");

  if (!workload || !sourceNode || !targetNode) {
    setFormStatus("Enter a workload, source node, and destination node before starting a migration.", "is-error");
    return;
  }
  if (sourceNode === targetNode) {
    setFormStatus("Choose different source and destination nodes.", "is-error");
    return;
  }

  button.disabled = true;
  button.textContent = "Starting migration…";
  setFormStatus("Creating the migration request…");

  const payload = { workload, sourceNode, targetNode };
  if (podName) payload.podName = podName;

  try {
    const result = await fetchJSON("/api/v1/migrations", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    setFormStatus(`Migration ${result.migration || "request"} has been created.`, "is-success");
    event.currentTarget.reset();
    await refresh({ force: true });
  } catch (error) {
    setFormStatus(`Migration was not started: ${error.message}`, "is-error");
  } finally {
    button.disabled = false;
    button.textContent = "Start migration";
  }
}

function initialize() {
  byID("refresh-button").addEventListener("click", () => refresh({ force: true }));
  byID("poll-toggle").addEventListener("click", togglePolling);
  byID("history-toggle").addEventListener("click", toggleHistory);
  byID("migration-form").addEventListener("submit", submitMigration);
  byID("pods-body").innerHTML = loadingRows(4);
  byID("migrations-body").innerHTML = loadingRows(4);
  byID("history-body").innerHTML = loadingRows(5);
  refresh();
  state.refreshTimer = window.setInterval(refresh, REFRESH_INTERVAL_MS);
}

initialize();
