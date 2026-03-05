import { api, apiPost } from '../api.js';
import { $, $$, badge, timeAgo, escapeHtml } from '../utils.js';
import { skeleton, makeSortable, confirmDialog, toast, cardHeader, attachPagination } from '../components.js';

const BAD_STATUSES = [
  'CrashLoopBackOff', 'Error', 'OOMKilled', 'ImagePullBackOff', 'ErrImagePull',
  'ContainerStatusUnknown', 'Evicted', 'Failed', 'Succeeded', 'Unknown',
  'Init:OOMKilled', 'CreateContainerConfigError',
];

function statusBadgeClass(status) {
  switch (status) {
    case 'CrashLoopBackOff': case 'OOMKilled': case 'Init:OOMKilled':
      return 'red';
    case 'Error': case 'Failed':
      return 'red';
    case 'Evicted': case 'ContainerStatusUnknown': case 'Unknown':
      return 'amber';
    case 'ImagePullBackOff': case 'ErrImagePull': case 'CreateContainerConfigError':
      return 'yellow';
    case 'Succeeded':
      return 'gray';
    default:
      return 'gray';
  }
}

// Unique key for a pod (used in the selection Set)
function podKey(p) {
  return p.namespace + '/' + p.name;
}

export async function renderActions(targetEl) {
  targetEl.innerHTML = skeleton(5);

  let data;
  try {
    data = await api('/actions/bad-pods');
  } catch (err) {
    targetEl.innerHTML = `<div class="error-msg">Failed to load bad pods: ${escapeHtml(err.message)}</div>`;
    return;
  }

  const pods = data.pods || [];
  const byNs = data.summary?.byNamespace || {};
  const byStatus = data.summary?.byStatus || {};
  const namespaces = Object.keys(byNs).sort();
  const statuses = Object.keys(byStatus).sort();

  // State
  let selectedNamespaces = new Set(namespaces);
  let selectedStatuses = new Set(statuses);
  const selectedPods = new Set();

  // Build page shell once — never rebuilt on filter change
  targetEl.innerHTML = `
    <div class="kpi-grid">
      <div class="kpi-card"><div class="label">Total Bad Pods</div><div class="value red">${pods.length}</div></div>
      ${namespaces.slice(0, 4).map(ns => `
        <div class="kpi-card"><div class="label">${escapeHtml(ns)}</div><div class="value">${byNs[ns]}</div></div>
      `).join('')}
    </div>

    <div class="card">
      ${cardHeader('Filters')}
      <div style="padding: 1rem; display: flex; gap: 2rem; flex-wrap: wrap;">
        <div>
          <strong>Namespaces</strong>
          <div style="margin-top: 0.5rem;">
            <button class="btn btn-gray btn-sm" id="ns-all">All</button>
            <button class="btn btn-gray btn-sm" id="ns-none">None</button>
          </div>
          <div id="ns-checks" style="margin-top: 0.5rem; display: flex; flex-wrap: wrap; gap: 0.5rem;">
            ${namespaces.map(ns => `
              <label style="display:flex;align-items:center;gap:0.25rem;cursor:pointer;">
                <input type="checkbox" data-ns="${escapeHtml(ns)}" checked>
                ${escapeHtml(ns)} <span class="badge badge-gray">${byNs[ns]}</span>
              </label>
            `).join('')}
          </div>
        </div>
        <div>
          <strong>Statuses</strong>
          <div style="margin-top: 0.5rem;">
            <button class="btn btn-gray btn-sm" id="st-all">All</button>
            <button class="btn btn-gray btn-sm" id="st-none">None</button>
          </div>
          <div id="st-checks" style="margin-top: 0.5rem; display: flex; flex-wrap: wrap; gap: 0.5rem;">
            ${statuses.map(st => `
              <label style="display:flex;align-items:center;gap:0.25rem;cursor:pointer;">
                <input type="checkbox" data-status="${escapeHtml(st)}" checked>
                ${badge(st, statusBadgeClass(st))} <span class="badge badge-gray">${byStatus[st]}</span>
              </label>
            `).join('')}
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      ${cardHeader(`Bad Pods (<span id="visible-count">${pods.length}</span>)`,
        `<button class="btn btn-red btn-sm" id="purge-btn" disabled>Purge Selected Pods</button>`
      )}
      <div class="table-wrap"><table id="bad-pods-table">
        <thead><tr>
          <th style="width:2rem"><input type="checkbox" id="select-all"></th>
          <th>Name</th><th>Namespace</th><th>Status</th><th>Node</th><th>Age</th>
        </tr></thead>
        <tbody>
          ${pods.map((p, i) => `<tr data-idx="${i}" data-ns="${escapeHtml(p.namespace)}" data-status="${escapeHtml(p.status)}">
            <td><input type="checkbox" class="pod-check" data-pod-key="${escapeHtml(podKey(p))}"></td>
            <td>${escapeHtml(p.name)}</td>
            <td>${escapeHtml(p.namespace)}</td>
            <td>${badge(p.status, statusBadgeClass(p.status))}</td>
            <td>${escapeHtml(p.node || '-')}</td>
            <td>${escapeHtml(p.age)}</td>
          </tr>`).join('')}
        </tbody>
      </table></div>
    </div>`;

  const table = $('#bad-pods-table');
  makeSortable(table);
  const pag = attachPagination(table);

  // --- Lightweight filter: toggle row visibility via data attributes ---
  function applyFilters() {
    const rows = $$('tbody tr', table);
    let visibleCount = 0;
    rows.forEach(row => {
      const ns = row.dataset.ns;
      const st = row.dataset.status;
      const show = selectedNamespaces.has(ns) && selectedStatuses.has(st);
      row.dataset.filtered = show ? '' : 'hide';
      row.style.display = show ? '' : 'none';
      if (show) visibleCount++;
    });
    // Prune selectedPods that are now filtered out
    for (const key of selectedPods) {
      const row = table.querySelector(`[data-pod-key="${CSS.escape(key)}"]`);
      if (row && row.closest('tr').dataset.filtered === 'hide') selectedPods.delete(key);
    }
    // Update count + pagination
    const countEl = document.getElementById('visible-count');
    if (countEl) countEl.textContent = visibleCount;
    if (pag) pag.refresh();
    updatePurgeBtn();
    syncCheckboxUI();
  }

  function updatePurgeBtn() {
    const btn = $('#purge-btn');
    if (btn) {
      const count = selectedPods.size;
      btn.textContent = count > 0 ? `Purge ${count} Pod${count > 1 ? 's' : ''}` : 'Purge Selected Pods';
      btn.disabled = count === 0;
    }
  }

  function syncCheckboxUI() {
    $$('.pod-check', table).forEach(cb => {
      cb.checked = selectedPods.has(cb.dataset.podKey);
    });
    const selectAll = $('#select-all');
    if (selectAll) {
      const visiblePods = pods.filter(p => selectedNamespaces.has(p.namespace) && selectedStatuses.has(p.status));
      selectAll.checked = visiblePods.length > 0 && visiblePods.every(p => selectedPods.has(podKey(p)));
    }
  }

  // Select-all checkbox
  $('#select-all')?.addEventListener('change', (e) => {
    const visiblePods = pods.filter(p => selectedNamespaces.has(p.namespace) && selectedStatuses.has(p.status));
    if (e.target.checked) {
      visiblePods.forEach(p => selectedPods.add(podKey(p)));
    } else {
      visiblePods.forEach(p => selectedPods.delete(podKey(p)));
    }
    syncCheckboxUI();
    updatePurgeBtn();
  });

  // Individual checkbox
  table.addEventListener('change', (e) => {
    if (!e.target.classList.contains('pod-check')) return;
    const key = e.target.dataset.podKey;
    if (e.target.checked) selectedPods.add(key); else selectedPods.delete(key);
    const selectAll = $('#select-all');
    if (selectAll) {
      const visiblePods = pods.filter(p => selectedNamespaces.has(p.namespace) && selectedStatuses.has(p.status));
      selectAll.checked = visiblePods.length > 0 && visiblePods.every(p => selectedPods.has(podKey(p)));
    }
    updatePurgeBtn();
  });

  // Purge button
  $('#purge-btn')?.addEventListener('click', () => {
    if (selectedPods.size === 0) return;
    const keyToPod = new Map();
    pods.forEach(p => keyToPod.set(podKey(p), p));
    const selected = [];
    for (const key of selectedPods) {
      if (keyToPod.has(key)) selected.push(keyToPod.get(key));
    }
    if (selected.length === 0) return;

    const nsSummary = {};
    selected.forEach(p => { nsSummary[p.namespace] = (nsSummary[p.namespace] || 0) + 1; });
    const detail = Object.entries(nsSummary).map(([ns, c]) => `${ns}: ${c}`).join(', ');
    confirmDialog(
      `Delete <strong>${selected.length}</strong> pod${selected.length > 1 ? 's' : ''}?<br><br>${detail}`,
      async () => {
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.style.cssText = 'cursor:wait;';
        overlay.innerHTML = `<div class="modal" style="text-align:center;min-width:320px">
          <div class="loading" style="padding:24px 0;font-size:14px;font-weight:500">Deleting ${selected.length} pod${selected.length > 1 ? 's' : ''}...</div>
          <div id="purge-progress" style="padding:0 20px 20px;font-size:12px;color:var(--text-muted)">Sending delete requests to cluster</div>
        </div>`;
        document.body.appendChild(overlay);

        try {
          const result = await apiPost('/actions/delete-pods', {
            pods: selected.map(p => ({ name: p.name, namespace: p.namespace })),
          });
          overlay.remove();

          const errCount = (result.errors || []).length;
          const deleted = result.deleted || 0;

          const resultOverlay = document.createElement('div');
          resultOverlay.className = 'modal-overlay';
          const errDetail = errCount > 0
            ? `<div style="margin-top:12px;font-size:12px;color:var(--red)">${result.errors.map(e => `<div>${escapeHtml(e.namespace)}/${escapeHtml(e.name)}: ${escapeHtml(e.error)}</div>`).join('')}</div>`
            : '';
          resultOverlay.innerHTML = `<div class="modal" style="min-width:360px">
            <div class="modal-header"><h3>Purge Complete</h3><button class="modal-close" onclick="this.closest('.modal-overlay').remove()">&times;</button></div>
            <div class="modal-body">
              <div style="display:flex;gap:16px;margin-bottom:12px">
                <div class="kpi-card" style="flex:1;animation:none;opacity:1"><div class="label">Deleted</div><div class="value green">${deleted}</div></div>
                <div class="kpi-card" style="flex:1;animation:none;opacity:1"><div class="label">Failed</div><div class="value ${errCount > 0 ? 'red' : ''}">${errCount}</div></div>
              </div>
              ${errDetail}
            </div>
            <div class="modal-actions"><button class="btn btn-blue" id="purge-done-btn">Done</button></div>
          </div>`;
          document.body.appendChild(resultOverlay);
          resultOverlay.querySelector('#purge-done-btn').onclick = () => {
            resultOverlay.remove();
            renderActions(targetEl);
          };
          resultOverlay.onclick = (e) => {
            if (e.target === resultOverlay) { resultOverlay.remove(); renderActions(targetEl); }
          };
        } catch (err) {
          overlay.remove();
          toast('Delete failed: ' + err.message, 'error');
        }
      }
    );
  });

  // Namespace filter handlers — only toggle visibility, never rebuild DOM
  function syncNsCheckboxes() {
    $$('#ns-checks input').forEach(cb => { cb.checked = selectedNamespaces.has(cb.dataset.ns); });
  }
  $('#ns-all')?.addEventListener('click', () => { selectedNamespaces = new Set(namespaces); syncNsCheckboxes(); applyFilters(); });
  $('#ns-none')?.addEventListener('click', () => { selectedNamespaces = new Set(); syncNsCheckboxes(); applyFilters(); });
  $$('#ns-checks input').forEach(cb => {
    cb.addEventListener('change', () => {
      const ns = cb.dataset.ns;
      if (cb.checked) selectedNamespaces.add(ns); else selectedNamespaces.delete(ns);
      applyFilters();
    });
  });

  // Status filter handlers
  function syncStCheckboxes() {
    $$('#st-checks input').forEach(cb => { cb.checked = selectedStatuses.has(cb.dataset.status); });
  }
  $('#st-all')?.addEventListener('click', () => { selectedStatuses = new Set(statuses); syncStCheckboxes(); applyFilters(); });
  $('#st-none')?.addEventListener('click', () => { selectedStatuses = new Set(); syncStCheckboxes(); applyFilters(); });
  $$('#st-checks input').forEach(cb => {
    cb.addEventListener('change', () => {
      const st = cb.dataset.status;
      if (cb.checked) selectedStatuses.add(st); else selectedStatuses.delete(st);
      applyFilters();
    });
  });
}
