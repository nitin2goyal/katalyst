import { api, apiPost } from '../api.js';
import { $, badge, escapeHtml, timeAgo, errorMsg } from '../utils.js';
import { skeleton, makeSortable, attachPagination, cardHeader, filterBar, attachFilterHandlers, confirmDialog, toast } from '../components.js';

const container = () => $('#page-container');

const tabDefs = [
  { id: 'overview', label: 'Overview' },
  { id: 'pdb', label: 'Blocking PDBs' },
  { id: 'events', label: 'Failed Events' },
  { id: 'single-replica', label: 'Single Replica + PDB' },
  { id: 'pods', label: 'Problem Pods' },
];

const subRenderers = {
  overview: renderOverview,
  pdb: renderPDBs,
  events: renderEvents,
  'single-replica': renderSingleReplica,
  pods: renderPods,
};

export async function renderScaleDown(params) {
  const activeTab = params?.tab || 'overview';

  container().innerHTML = `
    <div class="page-header">
      <h1>Scale Down Blockers</h1>
      <p>Consolidated view of everything preventing the cluster autoscaler from scaling down nodes</p>
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

  document.getElementById('page-tabs').addEventListener('click', (e) => {
    const btn = e.target.closest('.tab');
    if (!btn) return;
    const tabId = btn.dataset.tab;
    document.querySelectorAll('#page-tabs .tab').forEach(b => b.classList.remove('tab-active'));
    btn.classList.add('tab-active');
    history.replaceState(null, '', tabId === 'overview' ? '#/scaledown' : `#/scaledown/${tabId}`);
    switchTab(tabId);
  });

  await switchTab(activeTab);
}

// --- Overview Tab ---

function severityBadge(count, label, color) {
  if (count === 0) return `<div class="kpi-card"><div class="label">${label}</div><div class="value">${count}</div></div>`;
  return `<div class="kpi-card"><div class="label">${label}</div><div class="value ${color}">${count}</div></div>`;
}

async function renderOverview(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/scaledown/blockers');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load scale-down blockers: ' + err.message);
    return;
  }

  const s = data.summary || {};
  const failedEvents = data.failedEvents || [];
  const blockingPDBs = data.blockingPDBs || [];

  // Build per-node blocker summary from failed events
  const nodeBlockers = {};
  for (const ev of failedEvents) {
    const node = ev.nodeName || 'unknown';
    if (!nodeBlockers[node]) nodeBlockers[node] = { node, events: 0, pods: [], lastSeen: '' };
    nodeBlockers[node].events++;
    if (!nodeBlockers[node].lastSeen || ev.timestamp > nodeBlockers[node].lastSeen) {
      nodeBlockers[node].lastSeen = ev.timestamp;
    }
    for (const p of (ev.blockedBy || [])) {
      const key = `${p.namespace}/${p.pod}`;
      if (!nodeBlockers[node].pods.some(x => `${x.namespace}/${x.pod}` === key)) {
        nodeBlockers[node].pods.push(p);
      }
    }
  }
  const nodeList = Object.values(nodeBlockers).sort((a, b) => b.events - a.events);

  // PDB namespace breakdown
  const pdbByNS = {};
  for (const pdb of blockingPDBs) {
    pdbByNS[pdb.namespace] = (pdbByNS[pdb.namespace] || 0) + 1;
  }
  const topNamespaces = Object.entries(pdbByNS).sort((a, b) => b[1] - a[1]).slice(0, 10);

  targetEl.innerHTML = `
    <div class="kpi-grid">
      ${severityBadge(s.blockingPDBs || 0, 'Blocking PDBs', 'red')}
      ${severityBadge(s.failedEvents || 0, 'Failed Scale-Downs', 'red')}
      ${severityBadge(s.singleReplicaPDBs || 0, 'Single Replica + PDB', 'amber')}
      ${severityBadge(s.problematicPods || 0, 'Problem Pods', 'amber')}
      ${severityBadge(s.unevictablePods || 0, 'Unevictable Pods', 'yellow')}
      ${severityBadge(s.affectedNodes || 0, 'Affected Nodes', 'red')}
    </div>

    ${topNamespaces.length > 0 ? `
    <div class="card">
      ${cardHeader('Blocking PDBs by Namespace')}
      <div style="padding: 1rem;">
        ${topNamespaces.map(([ns, count]) => `
          <div style="display:flex;align-items:center;gap:0.5rem;margin-bottom:0.5rem;">
            <span style="min-width:200px;font-size:13px;color:var(--text-secondary)">${escapeHtml(ns)}</span>
            <div style="flex:1;background:var(--bg-tertiary);border-radius:4px;height:20px;overflow:hidden">
              <div style="width:${Math.min(100, count / (blockingPDBs.length || 1) * 100)}%;height:100%;background:var(--red);border-radius:4px;display:flex;align-items:center;padding-left:6px">
                <span style="font-size:11px;color:#fff;font-weight:600">${count}</span>
              </div>
            </div>
          </div>
        `).join('')}
      </div>
    </div>` : ''}

    ${nodeList.length > 0 ? `
    <div class="card">
      ${cardHeader('Nodes Failing Scale-Down (' + nodeList.length + ')')}
      <div class="table-wrap"><table id="node-blockers-table">
        <thead><tr>
          <th>Node</th>
          <th>Failed Events</th>
          <th>Blocking Pods</th>
          <th>Last Seen</th>
        </tr></thead>
        <tbody>
          ${nodeList.map(n => `<tr>
            <td>${n.node ? `<a href="#/nodes/${escapeHtml(n.node)}">${escapeHtml(shortNodeName(n.node))}</a>` : badge('unknown', 'gray')}</td>
            <td>${n.events}</td>
            <td style="max-width:500px">${n.pods.length > 0
              ? n.pods.map(p =>
                `${badge(p.reason, podReasonColor(p.reason))} <span style="font-size:12px;color:var(--text-muted)">${escapeHtml(p.namespace)}/${escapeHtml(p.pod)}</span>`
              ).join('<br>')
              : '<span style="color:var(--text-muted)">-</span>'}</td>
            <td title="${escapeHtml(n.lastSeen)}">${timeAgo(n.lastSeen)}</td>
          </tr>`).join('')}
        </tbody>
      </table></div>
    </div>` : ''}
  `;

  if (nodeList.length > 0) {
    makeSortable($('#node-blockers-table'));
  }
}

// --- Blocking PDBs Tab ---

async function renderPDBs(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/scaledown/blockers');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const pdbs = data.blockingPDBs || [];

  targetEl.innerHTML = `
    <div class="card">
      ${cardHeader('PDBs with 0 Disruptions Allowed (' + pdbs.length + ')')}
      ${filterBar({ placeholder: 'Search PDBs...', filters: [
        { id: 'ns-filter', label: 'Namespace', options: [...new Set(pdbs.map(p => p.namespace))].sort() }
      ]})}
      ${pdbs.length === 0
        ? '<div style="padding:2rem;text-align:center;color:var(--text-muted)">No blocking PDBs found</div>'
        : `<div class="table-wrap"><table id="pdb-table">
          <thead><tr>
            <th>Name</th>
            <th>Namespace</th>
            <th>Reason</th>
            <th>Expected Pods</th>
            <th>Healthy</th>
            <th>Desired Healthy</th>
            <th>Selector</th>
            <th>Age (days)</th>
          </tr></thead>
          <tbody>
            ${pdbs.map(p => `<tr data-ns="${escapeHtml(p.namespace)}">
              <td>${escapeHtml(p.name)}</td>
              <td>${escapeHtml(p.namespace)}</td>
              <td>${reasonBadge(p.reason)}</td>
              <td>${p.expectedPods}</td>
              <td>${p.currentHealthy}</td>
              <td>${p.desiredHealthy}</td>
              <td style="max-width:250px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${escapeHtml(p.selector || '')}">${escapeHtml(p.selector || '-')}</td>
              <td>${ageBadge(p.ageDays)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (pdbs.length > 0) {
    const table = $('#pdb-table');
    makeSortable(table);
    attachPagination(table);
    attachFilterHandlers($('.filter-bar'), table);
  }
}

function reasonBadge(reason) {
  if (reason === 'no-matching-pods') return badge('no matching pods', 'gray');
  if (reason === 'maxUnavailable=0') return badge(reason, 'red');
  if (reason.startsWith('minAvailable')) return badge(reason, 'amber');
  if (reason === 'at-minimum-healthy') return badge('at minimum', 'amber');
  return badge(reason, 'yellow');
}

function ageBadge(days) {
  if (days > 365) return badge(days + 'd', 'red');
  if (days > 90) return badge(days + 'd', 'amber');
  return badge(days + 'd', 'gray');
}

// --- Failed Events Tab ---

async function renderEvents(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/scaledown/blockers');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const events = data.failedEvents || [];
  const blockingPDBs = data.blockingPDBs || [];

  // Collect unique PDB names referenced in failed events
  const eventPodNS = new Set();
  for (const ev of events) {
    for (const p of (ev.blockedBy || [])) {
      if (p.reason === 'PDB violation' || p.reason === 'multiple PDBs') {
        eventPodNS.add(p.namespace);
      }
    }
  }
  // PDBs that are both blocking (disruptionsAllowed=0) AND in namespaces referenced by failed events
  const relevantPDBs = blockingPDBs.filter(p => eventPodNS.has(p.namespace));

  targetEl.innerHTML = `
    ${relevantPDBs.length > 0 ? `
    <div class="card" style="border-left:3px solid var(--red);margin-bottom:1rem">
      <div style="padding:1rem;display:flex;align-items:center;justify-content:space-between">
        <div style="display:flex;align-items:center;gap:0.75rem">
          <span style="font-size:1.25rem">&#9888;</span>
          <div>
            <strong>${relevantPDBs.length} blocking PDB${relevantPDBs.length > 1 ? 's' : ''} in affected namespaces</strong>
            <div style="font-size:12px;color:var(--text-muted);margin-top:2px">
              These PDBs have 0 disruptions allowed and are preventing node drain in the events below
            </div>
          </div>
        </div>
        <button class="btn btn-red" id="delete-blocking-pdbs-btn">Delete Blocking PDBs</button>
      </div>
    </div>` : ''}

    <div class="card">
      ${cardHeader('ScaleDownFailed Events (' + events.length + ')')}
      ${events.length === 0
        ? '<div style="padding:2rem;text-align:center;color:var(--text-muted)">No ScaleDownFailed events found</div>'
        : `<div class="table-wrap"><table id="events-table">
          <thead><tr>
            <th>Time</th>
            <th>Node</th>
            <th>Count</th>
            <th>Blocking Pods</th>
            <th>Details</th>
          </tr></thead>
          <tbody>
            ${events.map(e => `<tr>
              <td title="${escapeHtml(e.timestamp)}">${timeAgo(e.timestamp)}</td>
              <td>${e.nodeName ? `<a href="#/nodes/${escapeHtml(e.nodeName)}">${escapeHtml(shortNodeName(e.nodeName))}</a>` : badge('N/A', 'gray')}</td>
              <td>${e.count || 1}</td>
              <td>${(e.blockedBy || []).length > 0
                ? (e.blockedBy || []).map(p =>
                  `<div style="margin:2px 0">${badge(p.reason, podReasonColor(p.reason))} <span style="font-size:12px">${escapeHtml(p.namespace)}/${escapeHtml(p.pod)}</span></div>`
                ).join('')
                : `<span style="color:var(--text-muted);font-size:12px">${escapeHtml(truncate(e.message || '-', 80))}</span>`}</td>
              <td class="expandable-cell" style="max-width:400px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;cursor:pointer" title="Click to expand">${escapeHtml(e.message || '-')}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (events.length > 0) {
    const table = $('#events-table');
    makeSortable(table);
    attachPagination(table);
    attachExpandableCells(table);
  }

  // Delete blocking PDBs button
  const deleteBtn = document.getElementById('delete-blocking-pdbs-btn');
  if (deleteBtn && relevantPDBs.length > 0) {
    deleteBtn.addEventListener('click', () => {
      const pdbList = relevantPDBs.map(p => `${p.namespace}/${p.name}`);
      const listHtml = pdbList.length <= 15
        ? `<ul style="max-height:300px;overflow:auto;margin:0.5rem 0;padding-left:1.25rem;font-size:13px">${pdbList.map(p => `<li style="margin:2px 0">${escapeHtml(p)}</li>`).join('')}</ul>`
        : `<ul style="max-height:300px;overflow:auto;margin:0.5rem 0;padding-left:1.25rem;font-size:13px">${pdbList.slice(0, 15).map(p => `<li style="margin:2px 0">${escapeHtml(p)}</li>`).join('')}<li style="margin:2px 0;color:var(--text-muted)">...and ${pdbList.length - 15} more</li></ul>`;

      confirmDialog(
        `Delete <strong>${relevantPDBs.length}</strong> blocking PDB${relevantPDBs.length > 1 ? 's' : ''}?<br><br>` +
        `This will allow the cluster autoscaler to drain nodes blocked by these PDBs.` +
        listHtml,
        async () => {
          try {
            const result = await apiPost('/scaledown/delete-pdbs', {
              pdbs: relevantPDBs.map(p => ({ name: p.name, namespace: p.namespace }))
            });
            const msg = `Deleted ${result.deleted} PDB${result.deleted !== 1 ? 's' : ''}` +
              (result.errors?.length ? `, ${result.errors.length} failed` : '');
            toast(msg, result.errors?.length ? 'warning' : 'success');
            // Refresh the tab
            setTimeout(() => renderEvents(targetEl), 1000);
          } catch (err) {
            toast('Failed to delete PDBs: ' + err.message, 'error');
          }
        }
      );
    });
  }
}

// --- Single Replica + PDB Tab ---

async function renderSingleReplica(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/scaledown/blockers');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const items = data.singleReplicaPDBs || [];

  targetEl.innerHTML = `
    <div class="card" style="border-left:3px solid var(--amber);margin-bottom:1rem">
      <div style="padding:1rem;display:flex;align-items:center;gap:0.75rem">
        <span style="font-size:1.25rem">&#9888;</span>
        <div>
          <strong>Single-replica deployments with blocking PDBs</strong>
          <div style="font-size:12px;color:var(--text-muted);margin-top:2px">
            These deployments have exactly 1 replica and a PDB that prevents eviction.
            The autoscaler cannot drain nodes hosting these pods. Either increase replicas or remove the PDB.
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      ${cardHeader('Single Replica + PDB (' + items.length + ')')}
      ${items.length === 0
        ? '<div style="padding:2rem;text-align:center;color:var(--text-muted)">No single-replica deployments with blocking PDBs</div>'
        : `<div class="table-wrap"><table id="single-replica-table">
          <thead><tr>
            <th>Deployment</th>
            <th>Namespace</th>
            <th>Replicas</th>
            <th>Ready</th>
            <th>Matching PDB</th>
          </tr></thead>
          <tbody>
            ${items.map(d => `<tr>
              <td>${escapeHtml(d.name)}</td>
              <td>${escapeHtml(d.namespace)}</td>
              <td>${d.replicas}</td>
              <td>${d.readyReplicas === d.replicas
                ? badge(d.readyReplicas + '/' + d.replicas, 'green')
                : badge(d.readyReplicas + '/' + d.replicas, 'red')}</td>
              <td>${badge(d.matchingPDB, 'red')}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (items.length > 0) {
    const table = $('#single-replica-table');
    makeSortable(table);
    attachPagination(table);
  }
}

// --- Problem Pods Tab ---

async function renderPods(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/scaledown/blockers');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const problematic = data.problematicPods || [];
  const unevictable = data.unevictablePods || [];

  targetEl.innerHTML = `
    ${problematic.length > 0 ? `
    <div class="card">
      ${cardHeader('Problematic Pods (' + problematic.length + ')')}
      <div class="table-wrap"><table id="problematic-table">
        <thead><tr>
          <th>Pod</th>
          <th>Namespace</th>
          <th>Node</th>
          <th>Status</th>
          <th>Ready</th>
          <th>Restarts</th>
          <th>Age</th>
        </tr></thead>
        <tbody>
          ${problematic.map(p => `<tr>
            <td style="max-width:300px;overflow:hidden;text-overflow:ellipsis" title="${escapeHtml(p.name)}">${escapeHtml(p.name)}</td>
            <td>${escapeHtml(p.namespace)}</td>
            <td>${p.nodeName ? `<a href="#/nodes/${escapeHtml(p.nodeName)}">${escapeHtml(shortNodeName(p.nodeName))}</a>` : '-'}</td>
            <td>${statusBadge(p.status)}</td>
            <td>${escapeHtml(p.readiness)}</td>
            <td>${p.restarts > 10 ? badge(p.restarts, 'red') : p.restarts}</td>
            <td>${escapeHtml(p.age)}</td>
          </tr>`).join('')}
        </tbody>
      </table></div>
    </div>` : '<div class="card"><div style="padding:2rem;text-align:center;color:var(--text-muted)">No problematic pods found</div></div>'}

    ${unevictable.length > 0 ? `
    <div class="card">
      ${cardHeader('Unevictable Pods (' + unevictable.length + ')')}
      <div class="table-wrap"><table id="unevictable-table">
        <thead><tr>
          <th>Pod</th>
          <th>Namespace</th>
          <th>Node</th>
          <th>Reason</th>
          <th>Age</th>
        </tr></thead>
        <tbody>
          ${unevictable.map(p => `<tr>
            <td style="max-width:300px;overflow:hidden;text-overflow:ellipsis" title="${escapeHtml(p.name)}">${escapeHtml(p.name)}</td>
            <td>${escapeHtml(p.namespace)}</td>
            <td>${p.nodeName ? `<a href="#/nodes/${escapeHtml(p.nodeName)}">${escapeHtml(shortNodeName(p.nodeName))}</a>` : '-'}</td>
            <td>${unevictableReasonBadge(p.reason)}</td>
            <td>${escapeHtml(p.age)}</td>
          </tr>`).join('')}
        </tbody>
      </table></div>
    </div>` : ''}
  `;

  if (problematic.length > 0) {
    const t1 = $('#problematic-table');
    makeSortable(t1);
    attachPagination(t1);
  }
  if (unevictable.length > 0) {
    const t2 = $('#unevictable-table');
    makeSortable(t2);
    attachPagination(t2);
  }
}

function statusBadge(status) {
  if (status === 'CrashLoopBackOff') return badge(status, 'red');
  if (status === 'OOMKilled') return badge(status, 'red');
  if (status === 'Error' || status === 'Failed') return badge(status, 'red');
  if (status.startsWith('Init:')) return badge(status, 'amber');
  if (status === 'ImagePullBackOff' || status === 'ErrImagePull') return badge(status, 'amber');
  if (status === 'Running') return badge(status, 'green');
  return badge(status, 'yellow');
}

function unevictableReasonBadge(reason) {
  if (reason === 'local-storage') return badge('local storage', 'amber');
  if (reason === 'no-owner') return badge('no owner (standalone)', 'red');
  if (reason === 'mirror-pod') return badge('mirror pod', 'gray');
  return badge(reason, 'yellow');
}

function shortNodeName(name) {
  const parts = name.split('-');
  if (parts.length > 4) {
    return '...' + parts.slice(-3).join('-');
  }
  return name;
}

function podReasonColor(reason) {
  if (reason === 'PDB violation') return 'red';
  if (reason === 'multiple PDBs') return 'red';
  if (reason === 'delete failed') return 'amber';
  if (reason === 'eviction API not supported') return 'amber';
  return 'yellow';
}

function truncate(str, len) {
  if (!str || str.length <= len) return str || '';
  return str.slice(0, len) + '...';
}

function attachExpandableCells(table) {
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
