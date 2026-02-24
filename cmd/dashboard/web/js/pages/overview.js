import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, utilBar, healthDot, errorMsg } from '../utils.js';
import { renderGauge, destroyCharts } from '../charts.js';
import { skeleton, makeSortable, badge, cardHeader } from '../components.js';
import { renderEvents } from './events.js';
import { renderAudit } from './audit.js';

const container = () => $('#page-container');

function scoreColor(score) {
  if (score >= 8) return 'green';
  if (score >= 5) return 'amber';
  return 'red';
}

export async function renderOverview() {
  container().innerHTML = skeleton(5);
  try {
    const [summary, health, efficiency, score] = await Promise.all([
      api('/cluster/summary'),
      api('/cluster/health'),
      api('/cluster/efficiency').catch(() => null),
      api('/cluster/score').catch(() => null),
    ]);
    const s = summary || {};
    const controllerStatuses = (health && health.controllers) || {};
    const eff = efficiency || {};
    const sc = score || {};

    // Cluster Score (0-10) widget
    const overallScore = sc.overallScore != null ? sc.overallScore : null;
    const categories = sc.categories || {};
    const catNames = Object.keys(categories);
    const scoreHtml = overallScore != null ? `
      <div class="card cluster-score-card">
        <h2>Cluster Optimization Score</h2>
        <div class="score-layout">
          <div class="score-dial">
            <div class="score-circle ${scoreColor(overallScore)}">
              <span class="score-number">${overallScore.toFixed(1)}</span>
              <span class="score-max">/ 10</span>
            </div>
          </div>
          <div class="score-categories">
            ${catNames.map(cat => {
              const c = categories[cat];
              const catScore = c.score || 0;
              const pct = (catScore / 10) * 100;
              return `<div class="score-cat">
                <div class="score-cat-header">
                  <span class="score-cat-name">${cat.replace(/([A-Z])/g, ' $1').replace(/^./, s => s.toUpperCase())}</span>
                  <span class="score-cat-val ${scoreColor(catScore)}">${catScore}/10</span>
                </div>
                <div class="score-cat-bar"><div class="score-cat-fill ${scoreColor(catScore)}" style="width:${pct}%"></div></div>
                <div class="score-cat-detail">${c.details || ''}</div>
              </div>`;
            }).join('')}
          </div>
        </div>
      </div>` : '';

    const effScore = eff.score != null ? eff.score : null;
    const effGaugeHtml = effScore != null ? `
      <div class="card">
        <h2>Cluster Efficiency Score</h2>
        <div class="grid-2">
          <div class="chart-container"><canvas id="eff-gauge"></canvas></div>
          <div class="eff-breakdown">
            <div class="eff-item"><span class="eff-label">CPU Utilization</span><span class="eff-val">${fmtPct(eff.breakdown?.cpu)}</span></div>
            <div class="eff-item"><span class="eff-label">Memory Utilization</span><span class="eff-val">${fmtPct(eff.breakdown?.memory)}</span></div>
            <div class="eff-item"><span class="eff-label">Savings Captured</span><span class="eff-val">${fmtPct(eff.breakdown?.savings)}</span></div>
            <div class="eff-item"><span class="eff-label">Commitment Utilization</span><span class="eff-val">${fmtPct(eff.breakdown?.commitments)}</span></div>
          </div>
        </div>
      </div>` : '';

    container().innerHTML = `
      <div class="page-header"><h1>Cluster Overview</h1><p>Real-time cluster health and cost summary</p></div>
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Monthly Cost</div><div class="value">${fmt$(s.monthlyCostUSD)}</div><div class="sub">current month estimate</div></div>
        <div class="kpi-card"><div class="label">Potential Savings</div><div class="value green">${fmt$(s.potentialSavings)}</div><div class="sub">identified opportunities</div></div>
        <div class="kpi-card"><div class="label">Nodes</div><div class="value blue">${s.nodeCount || 0}</div><div class="sub">across ${s.nodeGroupCount || 0} groups</div></div>
        <div class="kpi-card"><div class="label">Pods</div><div class="value">${s.podCount || 0}</div><div class="sub">running workloads</div></div>
        <div class="kpi-card"><div class="label">Operating Mode</div><div class="value">${(s.mode || 'unknown').toUpperCase()}</div><div class="sub">${s.cloudProvider || ''}</div></div>
      </div>
      ${scoreHtml}
      ${effGaugeHtml}
      <div class="grid-2">
        <div class="card">
          <h2>CPU Utilization</h2>
          <div class="chart-container"><canvas id="cpu-gauge"></canvas></div>
        </div>
        <div class="card">
          <h2>Memory Utilization</h2>
          <div class="chart-container"><canvas id="mem-gauge"></canvas></div>
        </div>
      </div>
      <div class="grid-2">
        <div class="card">
          <h2>CPU Allocation</h2>
          <div class="chart-container"><canvas id="cpu-alloc-gauge"></canvas></div>
          <div class="alloc-detail">
            <span class="alloc-label">Requested vs Capacity</span>
            <span class="alloc-gap ${(s.cpuAllocationPct || 0) - (s.cpuUtilizationPct || 0) > 20 ? 'red' : 'amber'}">${((s.cpuAllocationPct || 0) - (s.cpuUtilizationPct || 0)).toFixed(1)}% over-provisioned</span>
          </div>
        </div>
        <div class="card">
          <h2>Memory Allocation</h2>
          <div class="chart-container"><canvas id="mem-alloc-gauge"></canvas></div>
          <div class="alloc-detail">
            <span class="alloc-label">Requested vs Capacity</span>
            <span class="alloc-gap ${(s.memAllocationPct || 0) - (s.memUtilizationPct || 0) > 20 ? 'red' : 'amber'}">${((s.memAllocationPct || 0) - (s.memUtilizationPct || 0)).toFixed(1)}% over-provisioned</span>
          </div>
        </div>
      </div>
      <div class="card">
        <h2>Controller Health</h2>
        <div class="health-grid" id="health-grid"></div>
      </div>
      <div class="card">
        <h2>Node Groups</h2>
        <div class="table-wrap"><table id="overview-ng-table">
          <thead><tr><th>Name</th><th>Instance Type</th><th>Nodes</th><th>CPU Util</th><th>Mem Util</th><th>Cost/mo</th></tr></thead>
          <tbody id="overview-ng-body"></tbody>
        </table></div>
      </div>
      <div class="card">
        ${cardHeader('Activity')}
        <div class="tabs" id="activity-tabs">
          <button class="tab tab-active" data-tab="events">Events</button>
          <button class="tab" data-tab="audit">Audit log</button>
        </div>
        <div id="activity-content"></div>
      </div>`;

    // Utilization gauges
    renderGauge('cpu-gauge', s.cpuUtilizationPct || 0, 'CPU');
    renderGauge('mem-gauge', s.memUtilizationPct || 0, 'Memory');

    // Allocation gauges
    renderGauge('cpu-alloc-gauge', s.cpuAllocationPct || 0, 'CPU Allocated');
    renderGauge('mem-alloc-gauge', s.memAllocationPct || 0, 'Memory Allocated');

    // Efficiency gauge
    if (effScore != null) {
      const effColor = effScore >= 80 ? '#10b981' : effScore >= 50 ? '#f59e0b' : '#ef4444';
      renderGauge('eff-gauge', effScore, 'Efficiency');
    }

    // Health grid
    const hg = $('#health-grid');
    const names = Object.keys(controllerStatuses);
    if (names.length === 0) {
      hg.innerHTML = '<div style="color:var(--text-muted);font-size:13px;">No controller status available</div>';
    } else {
      hg.innerHTML = names.map(n => {
        const st = controllerStatuses[n];
        return `<div class="health-item">${healthDot(st)}${n}</div>`;
      }).join('');
    }

    // Node groups mini-table
    try {
      const ngs = await api('/nodegroups');
      const list = toArray(ngs, 'nodeGroups');
      $('#overview-ng-body').innerHTML = list.length ? list.slice(0, 10).map(ng => `<tr class="clickable-row" onclick="location.hash='#/nodegroups/${ng.id || ''}'">
        <td>${ng.name || ''}</td><td>${ng.instanceType || ''}</td>
        <td>${ng.currentCount ?? 0}</td>
        <td>${utilBar(ng.cpuUtilPct)}</td><td>${utilBar(ng.memUtilPct)}</td>
        <td>${fmt$(ng.monthlyCostUSD)}</td>
      </tr>`).join('') : '<tr><td colspan="6" style="color:var(--text-muted)">No node groups found</td></tr>';
      makeSortable($('#overview-ng-table'));
    } catch (_) {
      $('#overview-ng-body').innerHTML = '<tr><td colspan="6" style="color:var(--text-muted)">No node group data</td></tr>';
    }

    // Activity tabs (Events / Audit)
    const activityContent = document.getElementById('activity-content');
    const activityRenderers = { events: renderEvents, audit: renderAudit };

    async function switchActivityTab(tabId) {
      activityContent.innerHTML = '';
      const render = activityRenderers[tabId];
      if (render) await render(activityContent);
    }

    document.getElementById('activity-tabs')?.addEventListener('click', (e) => {
      const btn = e.target.closest('.tab');
      if (!btn) return;
      const tabId = btn.dataset.tab;
      document.querySelectorAll('#activity-tabs .tab').forEach(b => b.classList.remove('tab-active'));
      btn.classList.add('tab-active');
      switchActivityTab(tabId);
    });

    await switchActivityTab('events');

  } catch (e) {
    container().innerHTML = errorMsg('Failed to load overview: ' + e.message);
  }
}
