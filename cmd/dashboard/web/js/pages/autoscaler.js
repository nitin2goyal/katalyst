import { api } from '../api.js';
import { $, $$, badge, fmtPct, fmt$, utilBar, escapeHtml, timeAgo, errorMsg, esc, fmtCPU, fmtMem } from '../utils.js';
import { skeleton, makeSortable, attachPagination, cardHeader, filterBar, attachFilterHandlers, exportCSV } from '../components.js';
import { addCleanup } from '../router.js';

const container = () => $('#page-container');

const tabDefs = [
  { id: 'status', label: 'Status' },
  { id: 'events', label: 'Events' },
  { id: 'overscaled', label: 'Over-Scaled' },
];

const subRenderers = {
  status: renderStatus,
  events: renderEvents,
  overscaled: renderOverscaled,
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
              <td><a href="#/nodes/${encodeURIComponent(n.name)}">${escapeHtml(n.name)}</a></td>
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
  if (action.includes('failed') || action.includes('unready') || action.includes('blocked')) return 'red';
  if (action.includes('scale-down') || action.includes('delete-unneeded') || action.includes('delete-unregistered') || action.includes('disrupting')) return 'green';
  if (action.includes('scale-up') || action.includes('triggered-scale-up') || action.includes('provisioned')) return 'blue';
  if (action.includes('drain') && !action.includes('failed')) return 'green';
  if (action.includes('not-needed') || action.includes('deferred') || action.includes('unconsolidatable')) return 'gray';
  if (action.includes('uncordon')) return 'amber';
  if (action.includes('dry-run')) return 'purple';
  if (action.includes('cordon')) return 'blue';
  return 'gray';
}

function sourceBadge(source) {
  if (source === 'ClusterAutoscaler') return badge('Autoscaler', 'blue');
  if (source === 'Karpenter') return badge('Karpenter', 'blue');
  if (source === 'KOptimizer') return badge('KOptimizer', 'purple');
  return badge(source || 'Unknown', 'gray');
}

async function renderEvents(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/autoscaler/events?pageSize=200');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load autoscaler events: ' + err.message);
    return;
  }

  const events = data.data || [];
  const total = data.total || events.length;

  targetEl.innerHTML = `
    <div class="card">
      ${cardHeader('Autoscaler Events (' + total + ' total)', `
        <div style="display:flex;gap:0.5rem">
          <button class="btn btn-gray btn-sm" id="export-events-csv">Export CSV</button>
          <button class="btn btn-gray btn-sm" id="copy-events-json">Copy JSON</button>
        </div>`)}
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
              <td>${e.target ? `<a href="#/nodes/${encodeURIComponent(e.target)}">${escapeHtml(e.target)}</a>` : '-'}</td>
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
    // Export CSV
    document.getElementById('export-events-csv')?.addEventListener('click', () => {
      exportCSV(
        ['Time', 'Source', 'Action', 'Target', 'User', 'Details'],
        events.map(e => [e.timestamp, e.source, e.action, e.target || '', e.user || '', e.details || '']),
        'katalyst-autoscaler-events.csv'
      );
    });

    // Copy JSON to clipboard
    document.getElementById('copy-events-json')?.addEventListener('click', () => {
      const btn = document.getElementById('copy-events-json');
      navigator.clipboard.writeText(JSON.stringify(events, null, 2)).then(() => {
        const orig = btn.textContent;
        btn.textContent = 'Copied!';
        setTimeout(() => { btn.textContent = orig; }, 2000);
      });
    });

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

// --- Over-Scaled Tab ---

function severityBadge(severity) {
  switch (severity) {
    case 'critical': return badge('Critical', 'red');
    case 'warning': return badge('Warning', 'amber');
    default: return badge('Info', 'blue');
  }
}

function causeIcon(cause) {
  const cl = cause.toLowerCase();
  if (cl.includes('metrics') || cl.includes('unavailable')) return 'Metrics';
  if (cl.includes('fallback')) return 'Fallback';
  if (cl.includes('pdb') || cl.includes('blocked')) return 'PDB';
  if (cl.includes('stuck') || cl.includes('last hpa scale')) return 'Stuck';
  if (cl.includes('paused')) return 'Paused';
  if (cl.includes('max replicas') || cl.includes('at max')) return 'At Max';
  if (cl.includes('minreplicas') || cl.includes('too high')) return 'Min High';
  if (cl.includes('not ready')) return 'Not Ready';
  if (cl.includes('inactive')) return 'Inactive';
  if (cl.includes('scalinglimited')) return 'Limited';
  if (cl.includes('cooldown') || cl.includes('dropped')) return 'Cooldown';
  return 'Config';
}

async function renderOverscaled(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/autoscaler/overscaled');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load over-scaled data: ' + err.message);
    return;
  }

  const summary = data.summary || {};
  const workloads = data.workloads || [];

  targetEl.innerHTML = `
    <div class="kpi-grid">
      <div class="kpi-card"><div class="label">Over-Scaled Workloads</div><div class="value ${summary.overscaledCount > 0 ? 'red' : 'green'}">${summary.overscaledCount || 0}</div></div>
      <div class="kpi-card"><div class="label">Excess Replicas</div><div class="value ${summary.totalExcessReplicas > 0 ? 'amber' : ''}">${summary.totalExcessReplicas || 0}</div></div>
      <div class="kpi-card"><div class="label">Wasted Cost/mo</div><div class="value red">${fmt$(summary.totalWastedCostUSD || 0)}</div><div class="sub">from over-scaling</div></div>
    </div>

    <div class="card">
      ${cardHeader('Over-Scaled Workloads', '<button class="btn btn-gray btn-sm" id="export-overscaled-csv">Export CSV</button>')}
      ${workloads.length === 0
        ? '<div style="padding: 2rem; text-align: center; color: var(--text-muted);">No over-scaled workloads detected. All autoscaled workloads are sized appropriately.</div>'
        : `
        ${filterBar({ placeholder: 'Search workloads...' })}
        <div class="table-wrap"><table id="overscaled-table">
          <thead><tr>
            <th>Severity</th>
            <th>Namespace</th>
            <th>Name</th>
            <th>Replicas</th>
            <th>Optimal</th>
            <th>Excess</th>
            <th>CPU Req/Pod</th>
            <th>Total CPU</th>
            <th>CPU Used</th>
            <th>CPU Eff.</th>
            <th>Mem Eff.</th>
            <th>Autoscaler</th>
            <th>Min</th>
            <th>Max</th>
            <th>Wasted/mo</th>
            <th>Root Cause</th>
          </tr></thead>
          <tbody>
            ${workloads.map(w => {
              const causes = w.rootCauses || [];
              const causeHtml = causes.length > 0
                ? causes.map(c => {
                    const cls = c.includes('metrics') || c.includes('unavailable') || c.includes('failing') ? 'red'
                      : c.includes('fallback') || c.includes('stuck') || c.includes('blocked') ? 'amber'
                      : c.includes('paused') || c.includes('too high') ? 'amber'
                      : 'gray';
                    return `<div style="margin:2px 0">${badge(causeIcon(c), cls)} <span style="font-size:11px">${esc(c)}</span></div>`;
                  }).join('')
                : '<span style="color:var(--text-muted);font-size:11px">Load dropped after scale-up</span>';
              return `<tr class="clickable-row" data-href="#/workloads/${encodeURIComponent(w.namespace)}/${encodeURIComponent(w.kind)}/${encodeURIComponent(w.name)}">
              <td>${severityBadge(w.severity)}</td>
              <td>${esc(w.namespace)}</td>
              <td>${esc(w.name)}</td>
              <td><strong>${w.currentReplicas}</strong></td>
              <td>${badge(String(w.optimalReplicas), 'green')}</td>
              <td>${badge('+' + w.excessReplicas, 'red')}</td>
              <td>${fmtCPU(parseInt(w.cpuRequestPerPod))}</td>
              <td>${fmtCPU(parseInt(w.totalCPURequest))}</td>
              <td>${fmtCPU(parseInt(w.totalCPUUsage))}</td>
              <td>${badge(fmtPct(w.cpuEfficiencyPct), w.cpuEfficiencyPct < 5 ? 'red' : w.cpuEfficiencyPct < 20 ? 'amber' : 'green')}</td>
              <td>${badge(fmtPct(w.memEfficiencyPct), w.memEfficiencyPct < 5 ? 'red' : w.memEfficiencyPct < 20 ? 'amber' : 'green')}</td>
              <td>${badge(w.autoscaler, 'blue')}</td>
              <td>${w.minReplicas}</td>
              <td>${w.maxReplicas}</td>
              <td><span class="red">${fmt$(w.wastedCostUSD)}</span></td>
              <td style="max-width:400px">${causeHtml}</td>
            </tr>`;
            }).join('')}
          </tbody>
        </table></div>`
      }
    </div>`;

  if (workloads.length > 0) {
    const table = $('#overscaled-table');
    makeSortable(table);
    const pag = attachPagination(table);

    const fb = targetEl.querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, table, pag);

    document.getElementById('export-overscaled-csv')?.addEventListener('click', () => {
      exportCSV(
        ['Severity', 'Namespace', 'Kind', 'Name', 'Current Replicas', 'Optimal Replicas',
         'Excess Replicas', 'CPU Req/Pod', 'Total CPU Req', 'Total CPU Used',
         'CPU Eff %', 'Mem Eff %', 'Autoscaler', 'HPA Name', 'Min', 'Max',
         'Monthly Cost', 'Wasted Cost', 'Reason', 'Root Causes'],
        workloads.map(w => [
          w.severity, w.namespace, w.kind, w.name,
          w.currentReplicas, w.optimalReplicas, w.excessReplicas,
          w.cpuRequestPerPod, w.totalCPURequest, w.totalCPUUsage,
          fmtPct(w.cpuEfficiencyPct), fmtPct(w.memEfficiencyPct),
          w.autoscaler, w.autoscalerName, w.minReplicas, w.maxReplicas,
          fmt$(w.monthlyCostUSD), fmt$(w.wastedCostUSD), w.reason,
          (w.rootCauses || []).join(' | ')
        ]),
        'katalyst-overscaled-workloads.csv'
      );
    });
  }
}
