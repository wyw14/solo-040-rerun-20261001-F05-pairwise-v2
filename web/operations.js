const { api, shortID, setMessage, withBusy } = window.LockFlow;
const form = document.querySelector("#plan-form");
const message = document.querySelector("#plan-message");
const migrateForm = document.querySelector("#migrate-form");
const migrateMessage = document.querySelector("#migrate-message");
const migrateResult = document.querySelector("#migrate-result");
const planSelect = document.querySelector("#migrate-plan");
const targetSelect = document.querySelector("#migrate-target");

let lastPlans = [];
let chamberIDs = [];

// Returns the 1-based position of a plan's first vessel in a chamber queue.
function planQueuePosition(queue, planID) {
  for (let index = 0; index < queue.length; index += 1) {
    if (queue[index].plan_id === planID) return index + 1;
  }
  return null;
}

async function refresh() {
  const [plans, chambers, health] = await Promise.all([api("/api/plans"), api("/api/chambers"), api("/healthz")]);
  document.querySelector("#health").textContent = health.status === "ok" ? "Devices online" : "Attention required";
  const queueByChamber = Object.fromEntries(chambers.map((item) => [item.chamber_id, item.queue || []]));
  chamberIDs = chambers.map((item) => item.chamber_id);
  lastPlans = Object.entries(plans).flatMap(([chamber, items]) => items.map((plan) => ({ ...plan, chamber })));
  lastPlans.sort((a, b) => new Date(a.created_at) - new Date(b.created_at));

  const rows = lastPlans.map((plan) => {
    const position = planQueuePosition(queueByChamber[plan.chamber] || [], plan.id);
    return `<tr><td>${plan.chamber}</td><td>${shortID(plan.id)}</td><td>${plan.direction}</td>`
      + `<td>${plan.vessels.length}</td><td>${position ? `#${position} of ${(queueByChamber[plan.chamber] || []).length}` : "-"}</td>`
      + `<td>${new Date(plan.created_at).toLocaleTimeString()}</td>`
      + `<td><button type="button" class="migrate-use" data-plan="${plan.id}">Migrate</button></td></tr>`;
  });
  document.querySelector("#plans").innerHTML = rows.join("") || '<tr><td colspan="7" class="muted">No active plans</td></tr>';

  const current = chambers.find((item) => item.cycle);
  document.querySelector("#metric-chamber").textContent = current?.chamber_id || "Idle";
  document.querySelector("#metric-stage").textContent = current?.cycle?.stage || "-";
  document.querySelector("#metric-cycle").textContent = shortID(current?.cycle?.id);

  const previousPlan = planSelect.value;
  planSelect.innerHTML = lastPlans.map((plan) =>
    `<option value="${plan.id}">${shortID(plan.id)} · ${plan.chamber} · ${plan.direction} · ${plan.vessels.length} vessel(s)</option>`).join("")
    || '<option value="">No active plans</option>';
  if (previousPlan && lastPlans.some((plan) => plan.id === previousPlan)) planSelect.value = previousPlan;
  fillTargets();
}

function fillTargets() {
  const selected = lastPlans.find((plan) => plan.id === planSelect.value);
  const previousTarget = targetSelect.value;
  targetSelect.innerHTML = "";
  if (!selected) return;
  const others = [...chamberIDs];
  others.filter((id) => id !== selected.chamber).sort().forEach((id) => {
    const option = document.createElement("option");
    option.value = id; option.textContent = id;
    targetSelect.appendChild(option);
  });
  if (previousTarget && [...targetSelect.options].some((option) => option.value === previousTarget)) {
    targetSelect.value = previousTarget;
  }
}

planSelect.addEventListener("change", () => { fillTargets(); migrateResult.innerHTML = ""; setMessage(migrateMessage, ""); });
document.querySelector("#plans").addEventListener("click", (event) => {
  const button = event.target.closest(".migrate-use");
  if (!button) return;
  planSelect.value = button.dataset.plan;
  fillTargets();
  migrateResult.scrollIntoView({ behavior: "smooth", block: "nearest" });
});

function renderChecks(checks) {
  return `<table><thead><tr><th>Check</th><th>Scope</th><th>Result</th><th>Reason</th></tr></thead><tbody>`
    + checks.map((check) => `<tr class="${check.passed ? "" : "check-failed"}">`
      + `<td>${check.name.replaceAll("_", " ")}</td><td>${check.scope}</td>`
      + `<td>${check.passed ? "✓ pass" : "✗ fail"}</td><td>${check.reason || ""}</td></tr>`).join("")
    + "</tbody></table>";
}

function renderMigration(result) {
  const positions = (result.vessels || []).map((item) => `<tr>`
    + `<td>${item.vessel.name} <span class="muted">${item.vessel.call_sign}</span></td>`
    + `<td>${result.from_chamber} #${item.from_position}</td>`
    + `<td class="arrow">→</td>`
    + `<td>${result.to_chamber} #${item.to_position}</td></tr>`).join("");
  const header = result.migrated
    ? `<div class="result-banner ok">Plan ${shortID(result.plan_id)} migrated ${result.from_chamber} → ${result.to_chamber} (${result.direction})</div>`
    : `<div class="result-banner fail">Migration rejected — ${(result.checks || []).filter((c) => !c.passed).length} check(s) failed</div>`;
  return `${header}`
    + (positions ? `<h3 class="result-title">Before / after queue positions</h3><table><thead><tr><th>Vessel</th><th>Before</th><th></th><th>After</th></tr></thead><tbody>${positions}</tbody></table>` : "")
    + `<h3 class="result-title">Precondition checks</h3>${renderChecks(result.checks || [])}`;
}

migrateForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = migrateForm.querySelector("button");
  await withBusy(button, async () => {
    migrateResult.innerHTML = "";
    try {
      const result = await api(`/api/plans/${planSelect.value}/migrate`, {
        method: "POST", body: JSON.stringify({ target_chamber: targetSelect.value }),
      });
      setMessage(migrateMessage, "");
      migrateResult.innerHTML = renderMigration(result);
      await refresh();
    } catch (error) {
      setMessage(migrateMessage, error.message);
      if (error.status === 422 && error.body && error.body.checks) {
        migrateResult.innerHTML = renderMigration(error.body);
      }
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
