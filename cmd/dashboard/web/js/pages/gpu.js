import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, utilBar, errorMsg, escapeHtml, badge, timeAgo } from '../utils.js';
import { skeleton, makeSortable, exportCSV, cardHeader } from '../components.js';

const GPU_TABS = [
  { id: 'nodes', label: 'Nodes' },
  { id: 'activity', label: 'Activity' },
];

export async function renderGPU(targetEl) {
  const container = () => targetEl || $('#page-container');
  const activeTab = 'nodes';

  container().innerHTML = `
    ${!targetEl ? '<div class="page-header"><h1>GPU Management</h1><p>GPU node utilization and optimization</p></div>' : ''}
    <div class="tabs" id="gpu-tabs">
      ${GPU_TABS.map(t => `<button class="tab ${t.id === activeTab ? 'tab-active' : ''}" data-tab="${t.id}">${t.label}</button>`).join('')}
    </div>
    <div id="gpu-tab-content"></div>`;

  const contentEl = document.getElementById('gpu-tab-content');

  async function switchTab(tabId) {
    contentEl.innerHTML = skeleton(5);
    if (tabId === 'nodes') await renderNodesTab(contentEl);
    else if (tabId === 'activity') await renderActivityTab(contentEl);
  }

  document.getElementById('gpu-tabs').addEventListener('click', (e) => {
    const btn = e.target.closest('.tab');
    if (!btn) return;
    const tabId = btn.dataset.tab;
    document.querySelectorAll('#gpu-tabs .tab').forEach(b => b.classList.remove('tab-active'));
    btn.classList.add('tab-active');
    switchTab(tabId);
  });

  await switchTab(activeTab);
}

async function renderNodesTab(contentEl) {
  try {
    const [nodesResp, util, recs] = await Promise.all([
      api('/gpu/nodes'), api('/gpu/utilization').catch(() => null), api('/gpu/recommendations').catch(() => null),
    ]);
    const nodeList = toArray(nodesResp, 'nodes', 'gpuNodes');
    const gpuConfig = nodesResp?.config || {};
    const utilData = util || {};
    const recList = toArray(recs, 'recommendations');

    const hasGPU = nodeList.length > 0 || (utilData.totalGPUs || 0) > 0;

    if (!hasGPU) {
      contentEl.innerHTML = `
        <div class="card" style="text-align:center;padding:48px 24px">
          <div style="font-size:48px;margin-bottom:16px;opacity:0.3">GPU</div>
          <h3 style="margin-bottom:8px">No GPU Nodes Detected</h3>
          <p style="color:var(--text-muted);max-width:480px;margin:0 auto;line-height:1.6">This cluster does not have any GPU-enabled nodes. GPU monitoring will automatically activate when GPU nodes (e.g., NVIDIA T4, A100, L4) are added to the cluster.</p>
        </div>`;
      return;
    }

    contentEl.innerHTML = `
      ${renderConfigCard(gpuConfig)}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total GPUs</div><div class="value purple">${utilData.totalGPUs || nodeList.length}</div></div>
        <div class="kpi-card"><div class="label">Used GPUs</div><div class="value">${utilData.usedGPUs || 0}</div></div>
        <div class="kpi-card"><div class="label">GPU Utilization</div><div class="value">${fmtPct(utilData.utilizationPct)}</div></div>
        <div class="kpi-card"><div class="label">GPU Recommendations</div><div class="value">${recList.length}</div></div>
      </div>
      <div class="card">
        ${cardHeader('GPU Nodes', '<button class="btn btn-gray btn-sm" onclick="window.__exportGpuCSV()">Export CSV</button>')}
        <div class="table-wrap"><table id="gpu-table">
          <thead><tr><th>Name</th><th>Instance Type</th><th>Status</th><th>GPUs</th><th>GPUs Used</th><th>CPU Headroom</th><th>CPU Util</th><th>Mem Util</th><th>Cost/hr</th></tr></thead>
          <tbody id="gpu-body"></tbody>
        </table></div>
      </div>
      ${recList.length ? `<div class="card"><h2>GPU Recommendations</h2>
        <div class="table-wrap"><table id="gpu-rec-table">
          <thead><tr><th>Type</th><th>Node</th><th>Description</th><th>Savings</th></tr></thead>
          <tbody id="gpu-rec-body"></tbody>
        </table></div></div>` : ''}`;

    $('#gpu-body').innerHTML = nodeList.length ? nodeList.map(n => `<tr class="clickable-row" onclick="location.hash='#/nodes/${n.name || ''}'">
      <td>${n.name || ''}</td><td>${n.instanceType || ''}</td>
      <td>${renderNodeStatusBadges(n)}</td>
      <td>${n.gpuCount ?? 0}</td><td>${n.gpuUsed ?? 0}</td>
      <td>${n.cpuHeadroomMillis ? n.cpuHeadroomMillis + 'm' : '-'}</td>
      <td>${utilBar(n.cpuUtilPct)}</td><td>${utilBar(n.memUtilPct)}</td>
      <td>${fmt$(n.hourlyCostUSD)}</td>
    </tr>`).join('') : '<tr><td colspan="9" style="color:var(--text-muted)">No GPU nodes</td></tr>';

    if (recList.length) {
      $('#gpu-rec-body').innerHTML = recList.map(r => `<tr>
        <td>${badge(r.type || '', 'purple')}</td><td>${r.target || r.node || ''}</td>
        <td>${escapeHtml(r.description || '')}</td><td class="value green">${fmt$(r.estimatedSavings)}</td>
      </tr>`).join('');
      makeSortable($('#gpu-rec-table'));
    }

    makeSortable($('#gpu-table'));

    window.__exportGpuCSV = () => {
      exportCSV(['Name', 'Instance Type', 'GPUs', 'GPUs Used', 'CPU Headroom', 'CPU Util %', 'Mem Util %', 'Cost/hr'],
        nodeList.map(n => [n.name, n.instanceType, n.gpuCount, n.gpuUsed, n.cpuHeadroomMillis || '', (n.cpuUtilPct||0).toFixed(1), (n.memUtilPct||0).toFixed(1), n.hourlyCostUSD]),
        'katalyst-gpu-nodes.csv');
    };
  } catch (e) {
    contentEl.innerHTML = errorMsg('Failed to load GPU data: ' + e.message);
  }
}

function renderConfigCard(cfg) {
  if (!cfg || !cfg.enabled) {
    return `<div class="card" style="padding:12px 16px;margin-bottom:16px;display:flex;align-items:center;gap:8px">
      ${badge('Disabled', 'gray')} <span style="color:var(--text-muted)">GPU optimizer is not enabled</span>
    </div>`;
  }
  const items = [
    { label: 'GPU Optimizer', on: cfg.enabled },
    { label: 'CPU Fallback', on: cfg.cpuFallbackEnabled },
    { label: 'CPU Scavenging', on: cfg.cpuScavengingEnabled },
    { label: 'Reclaim', on: cfg.reclaimEnabled },
  ];
  return `<div class="card" style="padding:12px 16px;margin-bottom:16px">
    <div style="display:flex;align-items:center;gap:12px;flex-wrap:wrap">
      <strong style="margin-right:4px">Config</strong>
      ${items.map(i => badge(i.label, i.on ? 'green' : 'gray')).join('')}
      ${cfg.idleThresholdPct ? `<span style="color:var(--text-muted);font-size:0.85em">Idle &lt; ${cfg.idleThresholdPct}% for ${cfg.idleDuration || '?'}</span>` : ''}
      ${cfg.scavengingCPUThresholdMillis ? `<span style="color:var(--text-muted);font-size:0.85em">Scavenge &ge; ${cfg.scavengingCPUThresholdMillis}m</span>` : ''}
      ${cfg.reclaimGracePeriod ? `<span style="color:var(--text-muted);font-size:0.85em">Reclaim grace: ${cfg.reclaimGracePeriod}</span>` : ''}
    </div>
  </div>`;
}

function renderNodeStatusBadges(n) {
  const badges = [];
  if (n.isFallback) badges.push(badge('Fallback', 'purple'));
  if (n.isScavenging) badges.push(badge('Scavenging', 'blue'));
  if (n.hasTaint) badges.push(badge('Tainted', 'gray'));
  if (!n.hasTaint && !n.isFallback && !n.isScavenging && (n.gpuUsed ?? 0) === 0) badges.push(badge('Idle', 'amber'));
  if (badges.length === 0) badges.push(badge('Active', 'green'));
  return badges.join(' ');
}

const ACTION_COLORS = {
  'enabled': 'green', 'disabled': 'amber', 'skipped': 'gray',
  'failed': 'red', 'detected': 'blue', 'recommend': 'blue',
  'complete': 'green', 'updated': 'green',
};

function actionBadgeColor(action) {
  for (const [key, color] of Object.entries(ACTION_COLORS)) {
    if (action.endsWith(key)) return color;
  }
  return 'purple';
}

async function renderActivityTab(contentEl) {
  try {
    const resp = await api('/gpu/activity?pageSize=200');
    const events = toArray(resp, 'data', 'events');

    if (!events.length) {
      contentEl.innerHTML = `<div class="card" style="text-align:center;padding:48px 24px">
        <h3 style="margin-bottom:8px">No GPU Activity Yet</h3>
        <p style="color:var(--text-muted)">GPU optimizer events will appear here as the system detects idle nodes, enables fallback, or scavenges CPU.</p>
      </div>`;
      return;
    }

    contentEl.innerHTML = `<div class="card">
      ${cardHeader('GPU Activity', `<span style="color:var(--text-muted);font-size:0.85em">${events.length} events</span>`)}
      <div class="table-wrap"><table id="gpu-activity-table">
        <thead><tr><th>Time</th><th>Action</th><th>Node</th><th>Details</th></tr></thead>
        <tbody id="gpu-activity-body"></tbody>
      </table></div>
    </div>`;

    const PAGE_SIZE = 50;
    let page = 0;

    function renderPage() {
      const start = page * PAGE_SIZE;
      const slice = events.slice(start, start + PAGE_SIZE);
      const totalPages = Math.ceil(events.length / PAGE_SIZE);

      $('#gpu-activity-body').innerHTML = slice.map(ev => `<tr>
        <td title="${ev.timestamp || ''}">${ev.timestamp ? timeAgo(ev.timestamp) : '-'}</td>
        <td>${badge(ev.action || '', actionBadgeColor(ev.action || ''))}</td>
        <td>${escapeHtml(ev.target || '')}</td>
        <td>${escapeHtml(ev.details || '')}</td>
      </tr>`).join('');

      // Pagination controls
      const existing = document.getElementById('gpu-activity-pager');
      if (existing) existing.remove();
      if (totalPages > 1) {
        const pager = document.createElement('div');
        pager.id = 'gpu-activity-pager';
        pager.style.cssText = 'display:flex;justify-content:center;gap:8px;padding:12px';
        pager.innerHTML = `
          <button class="btn btn-gray btn-sm" ${page === 0 ? 'disabled' : ''} data-dir="prev">Prev</button>
          <span style="line-height:32px;color:var(--text-muted)">${page + 1} / ${totalPages}</span>
          <button class="btn btn-gray btn-sm" ${page >= totalPages - 1 ? 'disabled' : ''} data-dir="next">Next</button>`;
        pager.addEventListener('click', (e) => {
          const dir = e.target.dataset.dir;
          if (dir === 'prev' && page > 0) { page--; renderPage(); }
          if (dir === 'next' && page < totalPages - 1) { page++; renderPage(); }
        });
        $('#gpu-activity-table').parentElement.after(pager);
      }
    }

    renderPage();
    makeSortable($('#gpu-activity-table'));
  } catch (e) {
    contentEl.innerHTML = errorMsg('Failed to load GPU activity: ' + e.message);
  }
}
