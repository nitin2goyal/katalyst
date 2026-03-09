import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, utilBar, errorMsg, escapeHtml, esc, badge, timeAgo, fmtCPUm, podStatusColor } from '../utils.js';
import { skeleton, makeSortable, exportCSV, cardHeader, richEmptyState } from '../components.js';

const GPU_TABS = [
  { id: 'nodes', label: 'Nodes' },
  { id: 'scavenging', label: 'Scavenging' },
  { id: 'activity', label: 'Activity' },
];

export async function renderGPU(targetEl) {
  const container = () => targetEl || $('#page-container');
  const activeTab = 'nodes';

  // Check if cluster has any GPUs before showing sub-tabs
  try {
    const [nodesResp, util] = await Promise.all([
      api('/gpu/nodes').catch(() => ({})),
      api('/gpu/utilization').catch(() => null),
    ]);
    const nodeList = toArray(nodesResp, 'nodes', 'gpuNodes');
    const hasGPU = nodeList.length > 0 || ((util?.totalGPUs) || 0) > 0;

    if (!hasGPU) {
      container().innerHTML = `
        ${!targetEl ? '<div class="page-header"><h1>GPU Management</h1><p>GPU node utilization and optimization</p></div>' : ''}
        ${richEmptyState('GPU', 'No GPU Nodes Detected',
          'This cluster does not have any GPU-enabled nodes. GPU monitoring will automatically activate when GPU nodes (e.g., NVIDIA T4, A100, L4) are added to the cluster.')}`;
      return;
    }
  } catch (_) { /* proceed with tabs on error */ }

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
    else if (tabId === 'scavenging') await renderScavengingTab(contentEl);
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
      contentEl.innerHTML = richEmptyState('GPU', 'No GPU Nodes Detected',
        'This cluster does not have any GPU-enabled nodes. GPU monitoring will automatically activate when GPU nodes (e.g., NVIDIA T4, A100, L4) are added to the cluster.');
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

    $('#gpu-body').innerHTML = nodeList.length ? nodeList.map(n => `<tr class="clickable-row" onclick="location.hash='#/nodes/${encodeURIComponent(n.name || '')}'">
      <td>${esc(n.name || '')}</td><td>${esc(n.instanceType || '')}</td>
      <td>${renderNodeStatusBadges(n)}</td>
      <td>${n.gpuCount ?? 0}</td><td>${n.gpuUsed ?? 0}</td>
      <td>${n.cpuHeadroomMillis ? n.cpuHeadroomMillis + 'm' : '-'}</td>
      <td>${utilBar(n.cpuUtilPct)}</td><td>${utilBar(n.memUtilPct)}</td>
      <td>${fmt$(n.hourlyCostUSD)}</td>
    </tr>`).join('') : '<tr><td colspan="9" style="color:var(--text-muted)">No GPU nodes</td></tr>';

    if (recList.length) {
      $('#gpu-rec-body').innerHTML = recList.map(r => `<tr>
        <td>${badge(r.type || '', 'purple')}</td><td>${esc(r.target || r.node || '')}</td>
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
    return `<div class="card config-card mb-4 flex-center">
      ${badge('Disabled', 'gray')} <span class="text-small-muted">GPU optimizer is not enabled</span>
    </div>`;
  }
  const items = [
    { label: 'GPU Optimizer', on: cfg.enabled },
    { label: 'CPU Fallback', on: cfg.cpuFallbackEnabled },
    { label: 'CPU Scavenging', on: cfg.cpuScavengingEnabled },
    { label: 'Reclaim', on: cfg.reclaimEnabled },
  ];
  const modeColor = cfg.mode === 'active' ? 'green' : cfg.mode === 'recommend' ? 'blue' : 'gray';
  return `<div class="card config-card mb-4">
    <div class="config-card-row">
      <strong>Config</strong>
      ${badge('Mode: ' + (cfg.mode || 'unknown'), modeColor)}
      ${items.map(i => badge(i.label, i.on ? 'green' : 'gray')).join('')}
      ${cfg.idleThresholdPct ? `<span class="text-small-muted">Idle &lt; ${cfg.idleThresholdPct}% for ${cfg.idleDuration || '?'}</span>` : ''}
      ${cfg.scavengingCPUThresholdMillis ? `<span class="text-small-muted">Scavenge &ge; ${cfg.scavengingCPUThresholdMillis}m</span>` : ''}
      ${cfg.reclaimGracePeriod ? `<span class="text-small-muted">Reclaim grace: ${cfg.reclaimGracePeriod}</span>` : ''}
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

async function renderScavengingTab(contentEl) {
  try {
    const resp = await api('/gpu/scavenging');
    const pods = resp.pods || [];
    const mode = resp.mode || 'unknown';
    const scavEnabled = resp.scavengingEnabled;
    const scavNodes = resp.scavengingNodeCount || 0;
    const totalHeadroom = resp.totalHeadroomMillis || 0;

    const modeColor = mode === 'active' ? 'green' : mode === 'recommend' ? 'blue' : 'gray';
    const modeWarning = mode !== 'active' ? `<div class="card" style="padding:12px 16px;margin-bottom:16px;border-left:3px solid var(--amber)">
      <strong style="color:var(--amber)">Mode is "${mode}"</strong> — scavenging recommendations are generated but NOT executed. Switch to <strong>Active</strong> mode (click the mode badge in the sidebar) to enable auto-execution.
    </div>` : '';

    contentEl.innerHTML = `
      ${modeWarning}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Mode</div><div class="value">${badge(mode, modeColor)}</div></div>
        <div class="kpi-card"><div class="label">Scavenging Nodes</div><div class="value blue">${scavNodes}</div></div>
        <div class="kpi-card"><div class="label">CPU Pods on GPU</div><div class="value purple">${pods.length}</div></div>
        <div class="kpi-card"><div class="label">Total Spare CPU</div><div class="value green">${totalHeadroom >= 1000 ? (totalHeadroom / 1000).toFixed(1) + ' cores' : totalHeadroom + 'm'}</div></div>
      </div>
      <div class="card">
        ${cardHeader('CPU Pods on GPU Nodes', `<span style="color:var(--text-muted);font-size:0.85em">${pods.length} pods</span>`)}
        <div class="table-wrap"><table id="scav-table">
          <thead><tr>
            <th>Pod</th><th>Namespace</th><th>Node</th><th>Instance</th><th>Ready</th>
            <th>CPU Req</th><th>CPU Used</th><th>Mem Req</th><th>Status</th>
            <th>Running For</th><th>Owner</th><th>Node Headroom</th>
          </tr></thead>
          <tbody id="scav-body"></tbody>
        </table></div>
      </div>`;

    $('#scav-body').innerHTML = pods.length ? pods.map(p => {
      const readyParts = (p.ready || '').split('/');
      const readyColor = readyParts[0] === readyParts[1] ? 'green' : 'amber';
      return `<tr class="clickable-row" onclick="location.hash='#/nodes/${encodeURIComponent(p.nodeName)}'">
        <td title="${escapeHtml(p.podName)}">${escapeHtml(p.podName.length > 45 ? p.podName.substring(0, 42) + '...' : p.podName)}</td>
        <td>${escapeHtml(p.namespace)}</td>
        <td title="${escapeHtml(p.nodeName)}">${escapeHtml(p.nodeName.replace(/^gke-intuition-gke-intuition-gke-/, ''))}</td>
        <td>${p.instanceType || ''}</td>
        <td><span style="color:var(--${readyColor});font-weight:500">${p.ready || '-'}</span></td>
        <td>${fmtCPUm(p.cpuRequestMillis || 0)}</td>
        <td>${p.cpuUsedMillis ? fmtCPUm(p.cpuUsedMillis) : '-'}</td>
        <td>${p.memRequestMi ? p.memRequestMi + ' Mi' : '-'}</td>
        <td>${badge(p.status || 'Unknown', podStatusColor(p.status))}</td>
        <td>${p.runningFor || '-'}</td>
        <td title="${escapeHtml(p.owner || '')}">${escapeHtml((p.owner || '').replace('ReplicaSet/', 'RS/'))}</td>
        <td>${fmtCPUm(p.nodeHeadroomMillis || 0)}</td>
      </tr>`;
    }).join('') : `<tr><td colspan="12" style="color:var(--text-muted)">No CPU pods found on GPU nodes${mode !== 'active' ? ' (mode is not active)' : ''}</td></tr>`;

    makeSortable($('#scav-table'));
  } catch (e) {
    contentEl.innerHTML = errorMsg('Failed to load scavenging data: ' + e.message);
  }
}

const ACTION_COLORS = {
  'enabled': 'green', 'disabled': 'amber', 'skipped': 'gray',
  'failed': 'red', 'detected': 'blue', 'recommend': 'blue',
  'complete': 'green', 'updated': 'green',
  'redistribute': 'purple', 'evacuate': 'amber',
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
