package gateway

import (
	_ "embed"

	"github.com/gofiber/fiber/v2"
)

// handleDashboard serves the admin observability dashboard at
// GET /api/v1/admin/dashboard. The page is a self-contained HTML file that
// fetches live data from the gateway's own JSON APIs (costs, rate-limit status,
// DLQ) and renders charts using Chart.js loaded from a CDN.
func (s *Server) handleDashboard(c *fiber.Ctx) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	c.Set("Cache-Control", "no-cache")
	return c.SendString(dashboardHTML)
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Soulacy — Admin Dashboard</title>
<script src="https://cdnjs.cloudflare.com/ajax/libs/Chart.js/4.4.1/chart.umd.min.js"></script>
<style>
  :root { --bg:#0f1117; --card:#1a1d26; --border:#2a2d3a; --text:#e2e8f0;
          --muted:#64748b; --accent:#6366f1; --green:#22c55e; --red:#ef4444;
          --yellow:#eab308; font-family:system-ui,sans-serif; }
  *{box-sizing:border-box;margin:0;padding:0}
  body{background:var(--bg);color:var(--text);min-height:100vh}
  header{padding:1.25rem 2rem;border-bottom:1px solid var(--border);
         display:flex;align-items:center;gap:1rem}
  header h1{font-size:1.1rem;font-weight:600;color:var(--text)}
  header span{font-size:.75rem;color:var(--muted);margin-left:auto}
  .grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));
        gap:1.25rem;padding:1.5rem 2rem}
  .card{background:var(--card);border:1px solid var(--border);border-radius:.75rem;
        padding:1.25rem}
  .card h2{font-size:.75rem;text-transform:uppercase;letter-spacing:.08em;
           color:var(--muted);margin-bottom:.75rem}
  .stat{font-size:2rem;font-weight:700;color:var(--accent)}
  .stat-label{font-size:.8rem;color:var(--muted);margin-top:.25rem}
  .badge{display:inline-block;padding:.2rem .55rem;border-radius:.35rem;
         font-size:.7rem;font-weight:600}
  .badge-green{background:#166534;color:var(--green)}
  .badge-red{background:#7f1d1d;color:var(--red)}
  .badge-yellow{background:#713f12;color:var(--yellow)}
  table{width:100%;border-collapse:collapse;font-size:.8rem}
  th{text-align:left;color:var(--muted);padding:.4rem 0;
     border-bottom:1px solid var(--border);font-weight:500}
  td{padding:.45rem 0;border-bottom:1px solid var(--border);color:var(--text)}
  td:last-child,th:last-child{text-align:right}
  .chart-wrap{position:relative;height:200px}
  .empty{color:var(--muted);font-size:.8rem;padding:.5rem 0}
  .refresh{background:none;border:1px solid var(--border);color:var(--muted);
           padding:.35rem .75rem;border-radius:.4rem;cursor:pointer;font-size:.78rem}
  .refresh:hover{border-color:var(--accent);color:var(--text)}
  #status-dot{width:.55rem;height:.55rem;border-radius:50%;background:var(--green);
               display:inline-block;margin-right:.4rem}
</style>
</head>
<body>
<header>
  <span id="status-dot"></span>
  <h1>Soulacy Admin Dashboard</h1>
  <span id="ts">Loading…</span>
  <button class="refresh" onclick="loadAll()">↻ Refresh</button>
</header>

<div class="grid">
  <!-- Rate limit status -->
  <div class="card" id="card-rl">
    <h2>Rate Limits</h2>
    <div id="rl-body"><span class="empty">Loading…</span></div>
  </div>

  <!-- Token usage today -->
  <div class="card">
    <h2>Token Usage — caller today</h2>
    <div id="token-body"><span class="empty">Loading…</span></div>
  </div>

  <!-- Cost breakdown -->
  <div class="card" style="grid-column:span 2">
    <h2>Cost by Agent</h2>
    <div id="cost-table"><span class="empty">Loading…</span></div>
  </div>

  <!-- Cost chart -->
  <div class="card" style="grid-column:span 2">
    <h2>Token Distribution</h2>
    <div class="chart-wrap"><canvas id="cost-chart"></canvas></div>
  </div>

  <!-- DLQ -->
  <div class="card" style="grid-column:span 2">
    <h2>Dead-Letter Queue</h2>
    <div id="dlq-body"><span class="empty">Loading…</span></div>
  </div>
</div>

<script>
const API = '/api/v1';
const key = document.cookie.match(/api_key=([^;]+)/)?.[1] || '';

async function apiFetch(path) {
  const h = key ? {Authorization: 'Bearer ' + key} : {};
  const r = await fetch(API + path, {headers: h});
  if (!r.ok) throw new Error(r.status + ' ' + path);
  return r.json();
}

let chartInst = null;

async function loadRateLimit() {
  try {
    const d = await apiFetch('/rate-limit/status');
    const el = document.getElementById('rl-body');
    if (!d.enabled) { el.innerHTML = '<span class="empty">Rate limiting disabled</span>'; return; }
    el.innerHTML = ` + "`" + `
      <table>
        <tr><th>Limit</th><th>Value</th></tr>
        <tr><td>Per-user RPM</td><td>${d.per_user_rpm || '—'}</td></tr>
        <tr><td>Per-agent RPM</td><td>${d.per_agent_rpm || '—'}</td></tr>
        <tr><td>Per-user tokens/day</td><td>${d.per_user_tokens_day || '—'}</td></tr>
        <tr><td>Per-agent tokens/day</td><td>${d.per_agent_tokens_day || '—'}</td></tr>
        <tr><td>Backend</td><td>${d.backend}</td></tr>
      </table>` + "`" + `;
    const tokenEl = document.getElementById('token-body');
    if (d.user) {
      const pct = d.per_user_tokens_day > 0
        ? Math.round(d.user.tokens_used / d.per_user_tokens_day * 100) : null;
      tokenEl.innerHTML = ` + "`" + `
        <div class="stat">${d.user.tokens_used.toLocaleString()}</div>
        <div class="stat-label">tokens used${pct !== null ? ' — ' + pct + '% of daily quota' : ''}</div>
        <div style="margin-top:.5rem;font-size:.78rem;color:var(--muted)">${d.user.id}</div>` + "`" + `;
    }
  } catch(e) {
    document.getElementById('rl-body').innerHTML = '<span class="empty">' + e.message + '</span>';
  }
}

async function loadCosts() {
  try {
    const d = await apiFetch('/costs');
    const rows = d.by_agent || [];
    const el = document.getElementById('cost-table');
    if (!rows.length) { el.innerHTML = '<span class="empty">No cost data yet</span>'; return; }
    el.innerHTML = ` + "`" + `<table>
      <tr><th>Agent</th><th>Tokens</th><th>Prompt</th><th>Completion</th><th>Cost (USD)</th></tr>
      ${rows.map(r => ` + "`" + `<tr>
        <td>${r.agent_id}</td>
        <td>${r.total_tokens.toLocaleString()}</td>
        <td>${r.prompt_tokens.toLocaleString()}</td>
        <td>${r.comp_tokens.toLocaleString()}</td>
        <td>$${r.cost_usd.toFixed(4)}</td>
      </tr>` + "`" + `).join('')}
    </table>` + "`" + `;

    // Chart
    const labels = rows.map(r => r.agent_id);
    const data   = rows.map(r => r.total_tokens);
    const ctx = document.getElementById('cost-chart').getContext('2d');
    if (chartInst) chartInst.destroy();
    chartInst = new Chart(ctx, {
      type: 'bar',
      data: {
        labels,
        datasets: [{
          label: 'Total tokens',
          data,
          backgroundColor: '#6366f180',
          borderColor: '#6366f1',
          borderWidth: 1,
          borderRadius: 4,
        }]
      },
      options: {
        responsive: true, maintainAspectRatio: false,
        plugins: { legend: { display: false } },
        scales: {
          x: { ticks: { color: '#64748b' }, grid: { color: '#2a2d3a' } },
          y: { ticks: { color: '#64748b' }, grid: { color: '#2a2d3a' } }
        }
      }
    });
  } catch(e) {
    document.getElementById('cost-table').innerHTML = '<span class="empty">' + e.message + '</span>';
  }
}

async function loadDLQ() {
  try {
    const d = await apiFetch('/admin/dlq');
    const items = d.items || [];
    const el = document.getElementById('dlq-body');
    if (!items.length) {
      el.innerHTML = '<span class="badge badge-green">Queue empty</span>';
      return;
    }
    el.innerHTML = ` + "`" + `
      <div style="margin-bottom:.75rem">
        <span class="badge badge-red">${items.length} failed job${items.length!==1?'s':''}</span>
      </div>
      <table>
        <tr><th>ID</th><th>Queue</th><th>Attempts</th><th>Error</th><th>Created</th></tr>
        ${items.slice(0,10).map(i => ` + "`" + `<tr>
          <td style="font-family:monospace;font-size:.7rem">${i.id}</td>
          <td>${i.queue}</td>
          <td>${i.attempts}</td>
          <td style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${i.error}</td>
          <td>${new Date(i.created_at).toLocaleString()}</td>
        </tr>` + "`" + `).join('')}
      </table>` + "`" + `;
  } catch(e) {
    document.getElementById('dlq-body').innerHTML = '<span class="empty">' + e.message + '</span>';
  }
}

async function loadAll() {
  document.getElementById('ts').textContent = 'Refreshing…';
  await Promise.allSettled([loadRateLimit(), loadCosts(), loadDLQ()]);
  document.getElementById('ts').textContent = 'Updated ' + new Date().toLocaleTimeString();
}

loadAll();
setInterval(loadAll, 30000); // auto-refresh every 30s
</script>
</body>
</html>
`
