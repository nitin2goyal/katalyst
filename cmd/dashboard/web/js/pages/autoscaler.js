import { api } from '../api.js';
import { $, $$, badge, fmtPct, fmt$, utilBar, escapeHtml, timeAgo, errorMsg } from '../utils.js';
import { skeleton, makeSortable, attachPagination, cardHeader, filterBar, attachFilterHandlers } from '../components.js';
import { addCleanup } from '../router.js';

const container = () => $('#page-container');

const tabDefs = [
  { id: 'status', label: 'Status' },
  { id: 'events', label: 'Events' },
];

const subRenderers = {
  status: renderStatus,
  events: renderEvents,
};

export async function renderAutoscaler(params) {
  const activeTab = params?.tab || 'status';

  container().innerHTML = `
    <div class="page-header">
      <h1>Autoscaler</h1>
      <p>Node removal pipeline status, blockers, and scaling events</p>
    </div>
    <div class="tabs" id="page-tabs">
      ${tabDefs.map(t => `<button class="tab ${t.id === activeTab ? 'tab-active' : ''}" data-tab="${t.id}">${t.label}</button>`).join('')}
    </div>
    <div id="content-area"></div>`;

  const contentEl = document.getElementById('content-area');

  async function switchTab(tabId) {
    contentEl.innerHTML = '';
    const render = subRenderers[tabId];
    if (render) await render(contentEl);
  }

  const tabHandler = (e) => {
    const btn = e.target.closest('.tab');
    if (!btn) return;
    const tabId = btn.dataset.tab;
    document.querySelectorAll('#page-tabs .tab').forEach(b => b.classList.remove('tab-active'));
    btn.classList.add('tab-active');
    history.replaceState(null, '', tabId === 'status' ? '#/autoscaler' : `#/autoscaler/${tabId}`);
    switchTab(tabId);
  };
  document.getElementById('page-tabs').addEventListener('click', tabHandler);
  addCleanup(() => document.getElementById('page-tabs')?.removeEventListener('click', tabHandler));

  await switchTab(activeTab);
}

// --- Status Tab ---

function configBadge(label, enabled, dryRun) {
  if (!enabled) return badge(label + ': OFF', 'red');
  if (dryRun) return badge(label + ': Dry-Run', 'amber');
  return badge(label + ': Live', 'green');
}

function modeBadge(mode) {
  switch (mode) {
    case 'active': return badge('Active', 'green');
    case 'recommend': return badge('Recommend', 'amber');
    case 'monitor': return badge('Monitor', 'gray');
    default: return badge(mode || 'unknown', 'gray');
  }
}

function blockerBadge(text) {
  if (text.includes('disabled')) return badge(text, 'red');
  if (text.includes('dry-run')) return badge(text, 'amber');
  if (text.includes('Mode is')) return badge(text, 'amber');
  if (text.includes('annotation')) return badge(text, 'gray');
  return badge(text, 'yellow');
}

function statusBadges(node) {
  const badges = [];
  if (node.isEmpty) badges.push(badge('Empty', 'blue'));
  if (node.isCordoned) {
    const who = node.cordonedBy === 'koptimizer' ? 'Cordoned (ours)' : 'Cordoned (external)';
    badges.push(badge(who, node.cordonedBy === 'koptimizer' ? 'purple' : 'amber'));
  }
  if (node.isUnderutilized && !node.isCordoned) badges.push(badge('Underutilized', 'yellow'));
  return badges.join(' ');
}

async function renderStatus(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/autoscaler/status');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load autoscaler status: ' + err.message);
    return;
  }

  const cfg = data.config || {};
  const summary = data.summary || {};
  const nodes = data.nodes || [];

  // Count blocked empty nodes for the alert banner
  const blockedEmpty = nodes.filter(n => n.isEmpty && n.removalBlockers && n.removalBlockers.length > 0);

  targetEl.innerHTML = `
    ${blockedEmpty.length > 0 ? `
      <div class="card" style="border-left: 3px solid var(--amber); margin-bottom: 1rem;">
        <div style="padding: 1rem; display: flex; align-items: center; gap: 0.75rem;">
          <span style="font-size: 1.25rem;">&#9888;</span>
          <div>
            <strong>${blockedEmpty.length} empty node${blockedEmpty.length > 1 ? 's' : ''} cannot be removed</strong>
            <div style="font-size: 12px; color: var(--text-muted); margin-top: 2px;">
              Review the blockers below to understand why
            </div>
          </div>
        </div>
      </div>
    ` : ''}

    <div class="kpi-grid">
      <div class="kpi-card"><div class="label">Total Nodes</div><div class="value">${summary.totalNodes || 0}</div></div>
      <div class="kpi-card"><div class="label">Empty Nodes</div><div class="value ${summary.emptyNodes > 0 ? 'amber' : ''}">${summary.emptyNodes || 0}</div></div>
      <div class="kpi-card"><div class="label">Cordoned Nodes</div><div class="value ${summary.cordonedNodes > 0 ? 'amber' : ''}">${summary.cordonedNodes || 0}</div></div>
      <div class="kpi-card"><div class="label">Blocked Nodes</div><div class="value ${summary.blockedNodes > 0 ? 'red' : ''}">${summary.blockedNodes || 0}</div></div>
      <div class="kpi-card"><div class="label">Underutilized</div><div class="value ${summary.underutilizedNodes > 0 ? 'yellow' : ''}">${summary.underutilizedNodes || 0}</div></div>
    </div>

    <div class="card">
      ${cardHeader('Configuration')}
      <div style="padding: 1rem; display: flex; flex-wrap: wrap; gap: 0.75rem; align-items: center;">
        ${modeBadge(cfg.mode)}
      </div>
    </div>

    <div class="card">
      ${cardHeader('Node Analysis (' + nodes.length + ' nodes)')}
      ${nodes.length === 0
        ? '<div style="padding: 2rem; text-align: center; color: var(--text-muted);">No empty, cordoned, or underutilized nodes found</div>'
        : `<div class="table-wrap"><table id="node-analysis-table">
          <thead><tr>
            <th>Name</th>
            <th>Node Group</th>
            <th>Status</th>
            <th>App Pods</th>
            <th>DS Pods</th>
            <th>CPU Alloc</th>
            <th>Mem Alloc</th>
            <th>Cost/hr</th>
            <th>Removal Blockers</th>
          </tr></thead>
          <tbody>
            ${nodes.map(n => `<tr>
              <td><a href="#/nodes/${escapeHtml(n.name)}">${escapeHtml(n.name)}</a></td>
              <td>${escapeHtml(n.nodeGroup || '-')}</td>
              <td>${statusBadges(n)}</td>
              <td>${n.appPodCount}</td>
              <td>${n.daemonSetPodCount}</td>
              <td>${utilBar(n.cpuAllocPct)}</td>
              <td>${utilBar(n.memAllocPct)}</td>
              <td>${fmt$(n.hourlyCostUSD)}</td>
              <td>${n.removalBlockers && n.removalBlockers.length > 0
                ? n.removalBlockers.map(b => blockerBadge(b)).join(' ')
                : badge('None', 'green')}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`
      }
    </div>`;

  if (nodes.length > 0) {
    const table = $('#node-analysis-table');
    makeSortable(table);
    attachPagination(table);
  }
}

// --- Events Tab ---

function actionBadgeClass(action) {
  if (action.includes('failed') || action.includes('unready')) return 'red';
  if (action.includes('scale-down') || action.includes('delete-unneeded') || action.includes('delete-unregistered')) return 'green';
  if (action.includes('scale-up') || action.includes('triggered-scale-up')) return 'blue';
  if (action.includes('drain') && !action.includes('failed')) return 'green';
  if (action.includes('not-needed') || action.includes('deferred')) return 'gray';
  if (action.includes('uncordon')) return 'amber';
  if (action.includes('dry-run')) return 'purple';
  if (action.includes('cordon')) return 'blue';
  return 'gray';
}

function sourceBadge(source) {
  return source === 'GKE'
    ? badge('GKE', 'blue')
    : badge('KOptimizer', 'purple');
}

async function renderEvents(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/autoscaler/events?pageSize=50');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load autoscaler events: ' + err.message);
    return;
  }

  const events = data.data || [];
  const total = data.total || events.length;

  targetEl.innerHTML = `
    <div class="card">
      ${cardHeader('Autoscaler Events (' + total + ' total)')}
      ${events.length === 0
        ? '<div style="padding: 2rem; text-align: center; color: var(--text-muted);">No autoscaler events found</div>'
        : `<div class="table-wrap"><table id="events-table">
          <thead><tr>
            <th>Time</th>
            <th>Source</th>
            <th>Action</th>
            <th>Target</th>
            <th>User</th>
            <th>Details</th>
          </tr></thead>
          <tbody>
            ${events.map(e => `<tr>
              <td title="${escapeHtml(e.timestamp)}">${timeAgo(e.timestamp)}</td>
              <td>${sourceBadge(e.source)}</td>
              <td>${badge(e.action, actionBadgeClass(e.action))}</td>
              <td>${e.target ? `<a href="#/nodes/${escapeHtml(e.target)}">${escapeHtml(e.target)}</a>` : '-'}</td>
              <td>${escapeHtml(e.user || '-')}</td>
              <td class="expandable-cell" style="max-width:400px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;cursor:pointer" title="Click to expand">${escapeHtml(e.details || '-')}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`
      }
    </div>`;

  if (events.length > 0) {
    const table = $('#events-table');
    makeSortable(table);
    attachPagination(table);
    // Click to expand/collapse details cells
    table.addEventListener('click', (e) => {
      const cell = e.target.closest('.expandable-cell');
      if (!cell) return;
      const isExpanded = cell.style.whiteSpace === 'normal';
      if (isExpanded) {
        cell.style.whiteSpace = 'nowrap';
        cell.style.overflow = 'hidden';
        cell.style.textOverflow = 'ellipsis';
        cell.title = 'Click to expand';
      } else {
        cell.style.whiteSpace = 'normal';
        cell.style.overflow = 'visible';
        cell.style.textOverflow = 'unset';
        cell.style.wordBreak = 'break-word';
        cell.title = 'Click to collapse';
      }
    });
  }
}
