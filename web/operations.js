const { api, shortID, setMessage, withBusy } = window.LockFlow;
const form = document.querySelector("#plan-form");
const message = document.querySelector("#plan-message");
const migrateMessage = document.querySelector("#migrate-message");

const escapeHTML = (value) => String(value).replace(/[&<>"']/g, (char) => (
  { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[char]
));

const otherChamber = (chamberID, chambers) =>
  chambers.map((item) => item.chamber_id).find((id) => id !== chamberID) || "";

// Plans whose lockage started or whose crew already saw a signal are locked to
// their chamber; the UI disables migration for them.
function planIsCommitted(plan, chambers) {
  const view = chambers.find((item) => item.chamber_id === plan.chamber_id);
  if (view?.cycle && view.cycle.plan_id === plan.id && view.cycle.stage !== "cancelled" && view.cycle.stage !== "completed" && view.cycle.stage !== "emergency") {
    return "in lockage (" + view.cycle.stage + ")";
  }
  const signaled = new Set((view?.signals || []).map((state) => state.vessel_id));
  if (plan.vessels.some((vessel) => signaled.has(vessel.id))) {
    return "signal published";
  }
  return "";
}

function renderPlans(plans, chambers) {
  const rows = Object.entries(plans).flatMap(([chamber, items]) => items.map((plan) => {
    const target = otherChamber(chamber, chambers);
    const committed = planIsCommitted(plan, chambers);
    const migrateCell = committed
      ? `<span class="badge blocked" title="Migration not allowed">${escapeHTML(committed)}</span>`
      : `<select data-role="target" aria-label="Target chamber"><option value="${target}">${target}</option></select>
         <button type="button" class="migrate" data-plan="${plan.id}" data-source="${chamber}">Migrate</button>`;
    return `<tr><td>${chamber}</td><td>${shortID(plan.id)}</td><td>${plan.direction}</td><td>${plan.vessels.length}</td><td>${new Date(plan.created_at).toLocaleTimeString()}</td><td><div class="migrate-cell">${migrateCell}</div></td></tr>`;
  }));
  document.querySelector("#plans").innerHTML = rows.join("") || '<tr><td colspan="6" class="muted">No active plans</td></tr>';
}

function renderQueues(chambers) {
  const panels = chambers.map((view) => {
    const rows = view.queue.map((entry, index) => {
      const state = entry.released ? '<span class="badge admitted">admitted</span>' : '<span class="badge waiting">waiting</span>';
      return `<tr><td>#${index}</td><td>${escapeHTML(entry.vessel.name)}</td><td>${escapeHTML(entry.vessel.call_sign)}</td><td>${shortID(entry.plan_id)}</td><td>${state}</td></tr>`;
    }).join("");
    const lockage = view.cycle ? `<div class="muted">Lockage ${shortID(view.cycle.id)} · ${view.cycle.stage} · ${view.cycle.direction}</div>` : '<div class="muted">No active lockage</div>';
    return `<div class="queue-panel"><h3>${view.chamber_id}</h3>${lockage}
      <table><thead><tr><th>Pos</th><th>Vessel</th><th>Call sign</th><th>Plan</th><th>State</th></tr></thead>
      <tbody>${rows || '<tr><td colspan="5" class="muted">Queue empty</td></tr>'}</tbody></table></div>`;
  });
  document.querySelector("#queues").innerHTML = panels.join("");
}

async function refresh() {
  const [plans, chambers, health] = await Promise.all([api("/api/plans"), api("/api/chambers"), api("/healthz")]);
  document.querySelector("#health").textContent = health.status === "ok" ? "Devices online" : "Attention required";
  renderPlans(plans, chambers);
  renderQueues(chambers);
}

function describeMigration(result) {
  const lines = result.positions.map((position) =>
    `${position.name}: ${result.source_chamber}#${position.source_index} → ${result.target_chamber}#${position.target_index}`
  );
  return `Migrated plan ${shortID(result.plan_id)} (${result.direction}), convoy order preserved:\n${lines.join("\n")}`;
}

document.querySelector("#plans").addEventListener("click", async (event) => {
  const button = event.target.closest("button.migrate");
  if (!button) return;
  const row = button.closest("tr");
  const target = row.querySelector('select[data-role="target"]').value;
  setMessage(migrateMessage, "");
  await withBusy(button, async () => {
    try {
      const result = await api(`/api/plans/${button.dataset.plan}/migrate`, {
        method: "POST",
        body: JSON.stringify({ target_chamber_id: target }),
      });
      setMessage(migrateMessage, describeMigration(result));
      migrateMessage.className = "message success";
      await refresh();
    } catch (error) {
      const reasons = error.reasons?.length ? `\n• ${error.reasons.join("\n• ")}` : "";
      setMessage(migrateMessage, `Migration failed${reasons ? ":" : " — " + error.message}${reasons}`);
      migrateMessage.className = "message";
    }
  });
});

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = form.querySelector("button");
  await withBusy(button, async () => {
    try {
      const result = await api("/api/plans", { method: "POST", body: JSON.stringify({
        chamber_id: document.querySelector("#chamber").value,
        direction: document.querySelector("#direction").value,
        vessels: [{ name: document.querySelector("#vessel").value, call_sign: document.querySelector("#call-sign").value, length_m: Number(document.querySelector("#length").value) }],
      }) });
      setMessage(message, `Plan ${shortID(result.plan.id)} admitted to ${result.plan.chamber_id}.`);
      await refresh();
    } catch (error) { setMessage(message, error.message); }
  });
});

refresh().catch((error) => setMessage(message, error.message));
setInterval(() => refresh().catch(() => {}), 5000);
