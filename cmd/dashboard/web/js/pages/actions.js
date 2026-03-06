import { api, apiPost } from '../api.js';
import { $, $$, badge, escapeHtml } from '../utils.js';
import { skeleton, makeSortable, confirmDialog, toast, cardHeader, attachPagination } from '../components.js';

const BAD_STATUSES = [
  'CrashLoopBackOff', 'Error', 'OOMKilled', 'ImagePullBackOff', 'ErrImagePull',
  'ContainerStatusUnknown', 'Evicted', 'Failed', 'Succeeded', 'Completed', 'Unknown',
  'CreateContainerConfigError',
  'Init:OOMKilled', 'Init:CrashLoopBackOff', 'Init:Error',
  'Init:ImagePullBackOff', 'Init:ErrImagePull', 'Init:ContainerStatusUnknown',
  'Init:CreateContainerConfigError',
];

function statusBadgeClass(status) {
  switch (status) {
    case 'CrashLoopBackOff': case 'OOMKilled':
    case 'Init:OOMKilled': case 'Init:CrashLoopBackOff':
    case 'Error': case 'Failed': case 'Init:Error':
      return 'red';
    case 'Evicted': case 'ContainerStatusUnknown': case 'Unknown':
    case 'Init:ContainerStatusUnknown':
      return 'amber';
    case 'ImagePullBackOff': case 'ErrImagePull': case 'CreateContainerConfigError':
    case 'Init:ImagePullBackOff': case 'Init:ErrImagePull': case 'Init:CreateContainerConfigError':
      return 'yellow';
    case 'Succeeded': case 'Completed':
      return 'gray';
    default:
      return 'gray';
  }
}

function reasonBadgeClass(reason) {
  switch (reason) {
    case 'Orphaned': return 'red';
    case 'Stuck': return 'amber';
    case 'Stale': return 'gray';
    default: return 'gray';
  }
}

function podKey(p) { return p.namespace + '/' + p.name; }
function rsKey(r) { return r.namespace + '/' + r.name; }

// ─── Main entry ─────────────────────────────────────────────────────────
export async function renderActions(targetEl) {
  targetEl.innerHTML = `
    <div class="tabs" id="actions-tabs">
      <button class="tab tab-active" data-tab="bad-pods">Bad Pods</button>
      <button class="tab" data-tab="bad-rs">Bad ReplicaSets</button>
    </div>
    <div id="actions-content"></div>`;

  const contentEl = targetEl.querySelector('#actions-content');
  let activeTab = 'bad-pods';

  async function switchTab(tabId) {
    activeTab = tabId;
    contentEl.innerHTML = '';
    if (tabId === 'bad-pods') await renderBadPods(contentEl);
    else await renderBadReplicaSets(contentEl);
  }

  targetEl.querySelector('#actions-tabs').addEventListener('click', (e) => {
    const btn = e.target.closest('.tab');
    if (!btn) return;
    const tabId = btn.dataset.tab;
    if (tabId === activeTab) return;
    targetEl.querySelectorAll('#actions-tabs .tab').forEach(b => b.classList.remove('tab-active'));
    btn.classList.add('tab-active');
    switchTab(tabId);
  });

  await switchTab('bad-pods');
}

// ─── Bad Pods Tab ───────────────────────────────────────────────────────
async function renderBadPods(contentEl) {
  contentEl.innerHTML = skeleton(5);

  let data;
  try {
    data = await api('/actions/bad-pods');
  } catch (err) {
    contentEl.innerHTML = `<div class="error-msg">Failed to load bad pods: ${escapeHtml(err.message)}</div>`;
    return;
  }

  const pods = data.pods || [];
  const byNs = data.summary?.byNamespace || {};
  const byStatus = data.summary?.byStatus || {};
  const namespaces = Object.keys(byNs).sort();
  const statuses = Object.keys(byStatus).sort();

  let selectedNamespaces = new Set(namespaces);
  let selectedStatuses = new Set(statuses);
  const selectedPods = new Set();

  contentEl.innerHTML = `
    <div class="kpi-grid">
      <div class="kpi-card"><div class="label">Total Bad Pods</div><div class="value red">${pods.length}</div></div>
      ${namespaces.slice(0, 4).map(ns => `
        <div class="kpi-card"><div class="label">${escapeHtml(ns)}</div><div class="value">${byNs[ns]}</div></div>
      `).join('')}
    </div>

    <div class="card">
      ${cardHeader('Filters')}
      <div class="filter-section">
        <div>
          <strong>Namespaces</strong>
          <div class="filter-group-btns">
            <button class="btn btn-gray btn-sm" id="ns-all">All</button>
            <button class="btn btn-gray btn-sm" id="ns-none">None</button>
          </div>
          <div id="ns-checks" class="filter-group-btns flex-wrap-gap">
            ${namespaces.map(ns => `
              <label class="filter-check-label">
                <input type="checkbox" data-ns="${escapeHtml(ns)}" checked>
                ${escapeHtml(ns)} <span class="badge badge-gray">${byNs[ns]}</span>
              </label>
            `).join('')}
          </div>
        </div>
        <div>
          <strong>Statuses</strong>
          <div class="filter-group-btns">
            <button class="btn btn-gray btn-sm" id="st-all">All</button>
            <button class="btn btn-gray btn-sm" id="st-none">None</button>
          </div>
          <div id="st-checks" class="filter-group-btns flex-wrap-gap">
            ${statuses.map(st => `
              <label class="filter-check-label">
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
    for (const key of selectedPods) {
      const row = table.querySelector(`[data-pod-key="${CSS.escape(key)}"]`);
      if (row && row.closest('tr').dataset.filtered === 'hide') selectedPods.delete(key);
    }
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
    $$('.pod-check', table).forEach(cb => { cb.checked = selectedPods.has(cb.dataset.podKey); });
    const selectAll = $('#select-all');
    if (selectAll) {
      const visiblePods = pods.filter(p => selectedNamespaces.has(p.namespace) && selectedStatuses.has(p.status));
      selectAll.checked = visiblePods.length > 0 && visiblePods.every(p => selectedPods.has(podKey(p)));
    }
  }

  $('#select-all')?.addEventListener('change', (e) => {
    const visiblePods = pods.filter(p => selectedNamespaces.has(p.namespace) && selectedStatuses.has(p.status));
    if (e.target.checked) visiblePods.forEach(p => selectedPods.add(podKey(p)));
    else visiblePods.forEach(p => selectedPods.delete(podKey(p)));
    syncCheckboxUI();
    updatePurgeBtn();
  });

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

  $('#purge-btn')?.addEventListener('click', () => {
    if (selectedPods.size === 0) return;
    const keyToPod = new Map();
    pods.forEach(p => keyToPod.set(podKey(p), p));
    const selected = [];
    for (const key of selectedPods) { if (keyToPod.has(key)) selected.push(keyToPod.get(key)); }
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
          const allPods = selected.map(p => ({ name: p.name, namespace: p.namespace }));
          const BATCH = 500;
          let totalDeleted = 0;
          let allErrors = [];
          for (let i = 0; i < allPods.length; i += BATCH) {
            const batch = allPods.slice(i, i + BATCH);
            const progressEl = overlay.querySelector('#purge-progress');
            if (progressEl && allPods.length > BATCH) {
              progressEl.textContent = `Batch ${Math.floor(i / BATCH) + 1} of ${Math.ceil(allPods.length / BATCH)}...`;
            }
            const res = await apiPost('/actions/delete-pods', { pods: batch });
            totalDeleted += res.deleted || 0;
            if (res.errors) allErrors = allErrors.concat(res.errors);
          }
          overlay.remove();
          showPurgeResult(totalDeleted, allErrors, () => renderBadPods(contentEl));
        } catch (err) {
          overlay.remove();
          toast('Delete failed: ' + err.message, 'error');
        }
      }
    );
  });

  function syncNsCheckboxes() { $$('#ns-checks input').forEach(cb => { cb.checked = selectedNamespaces.has(cb.dataset.ns); }); }
  $('#ns-all')?.addEventListener('click', () => { selectedNamespaces = new Set(namespaces); syncNsCheckboxes(); applyFilters(); });
  $('#ns-none')?.addEventListener('click', () => { selectedNamespaces = new Set(); syncNsCheckboxes(); applyFilters(); });
  $$('#ns-checks input').forEach(cb => {
    cb.addEventListener('change', () => {
      if (cb.checked) selectedNamespaces.add(cb.dataset.ns); else selectedNamespaces.delete(cb.dataset.ns);
      applyFilters();
    });
  });

  function syncStCheckboxes() { $$('#st-checks input').forEach(cb => { cb.checked = selectedStatuses.has(cb.dataset.status); }); }
  $('#st-all')?.addEventListener('click', () => { selectedStatuses = new Set(statuses); syncStCheckboxes(); applyFilters(); });
  $('#st-none')?.addEventListener('click', () => { selectedStatuses = new Set(); syncStCheckboxes(); applyFilters(); });
  $$('#st-checks input').forEach(cb => {
    cb.addEventListener('change', () => {
      if (cb.checked) selectedStatuses.add(cb.dataset.status); else selectedStatuses.delete(cb.dataset.status);
      applyFilters();
    });
  });
}

// ─── Bad ReplicaSets Tab ────────────────────────────────────────────────
async function renderBadReplicaSets(contentEl) {
  contentEl.innerHTML = skeleton(5);

  let data;
  try {
    data = await api('/actions/bad-replicasets');
  } catch (err) {
    contentEl.innerHTML = `<div class="error-msg">Failed to load bad replicasets: ${escapeHtml(err.message)}</div>`;
    return;
  }

  const rsList = data.replicaSets || [];
  const byNs = data.summary?.byNamespace || {};
  const byReason = data.summary?.byReason || {};
  const namespaces = Object.keys(byNs).sort();
  const reasons = Object.keys(byReason).sort();

  let selectedNamespaces = new Set(namespaces);
  let selectedReasons = new Set(reasons);
  const selectedRS = new Set();

  contentEl.innerHTML = `
    <div class="kpi-grid">
      <div class="kpi-card"><div class="label">Total Bad ReplicaSets</div><div class="value red">${rsList.length}</div></div>
      ${reasons.map(r => `
        <div class="kpi-card"><div class="label">${escapeHtml(r)}</div><div class="value">${byReason[r]}</div></div>
      `).join('')}
    </div>

    <div class="card">
      ${cardHeader('Filters')}
      <div class="filter-section">
        <div>
          <strong>Namespaces</strong>
          <div class="filter-group-btns">
            <button class="btn btn-gray btn-sm" id="rs-ns-all">All</button>
            <button class="btn btn-gray btn-sm" id="rs-ns-none">None</button>
          </div>
          <div id="rs-ns-checks" class="filter-group-btns flex-wrap-gap">
            ${namespaces.map(ns => `
              <label class="filter-check-label">
                <input type="checkbox" data-ns="${escapeHtml(ns)}" checked>
                ${escapeHtml(ns)} <span class="badge badge-gray">${byNs[ns]}</span>
              </label>
            `).join('')}
          </div>
        </div>
        <div>
          <strong>Reason</strong>
          <div class="filter-group-btns">
            <button class="btn btn-gray btn-sm" id="rs-reason-all">All</button>
            <button class="btn btn-gray btn-sm" id="rs-reason-none">None</button>
          </div>
          <div id="rs-reason-checks" class="filter-group-btns flex-wrap-gap">
            ${reasons.map(r => `
              <label class="filter-check-label">
                <input type="checkbox" data-reason="${escapeHtml(r)}" checked>
                ${badge(r, reasonBadgeClass(r))} <span class="badge badge-gray">${byReason[r]}</span>
              </label>
            `).join('')}
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      ${cardHeader(`Bad ReplicaSets (<span id="rs-visible-count">${rsList.length}</span>)`,
        `<button class="btn btn-red btn-sm" id="rs-purge-btn" disabled>Purge Selected</button>`
      )}
      <div class="table-wrap"><table id="bad-rs-table">
        <thead><tr>
          <th style="width:2rem"><input type="checkbox" id="rs-select-all"></th>
          <th>Name</th><th>Namespace</th><th>Reason</th><th>Owner</th><th>Replicas</th><th>Ready</th><th>Age</th>
        </tr></thead>
        <tbody>
          ${rsList.map((rs, i) => `<tr data-idx="${i}" data-ns="${escapeHtml(rs.namespace)}" data-reason="${escapeHtml(rs.reason)}">
            <td><input type="checkbox" class="rs-check" data-rs-key="${escapeHtml(rsKey(rs))}"></td>
            <td>${escapeHtml(rs.name)}</td>
            <td>${escapeHtml(rs.namespace)}</td>
            <td>${badge(rs.reason, reasonBadgeClass(rs.reason))}</td>
            <td>${escapeHtml(rs.owner || '-')}</td>
            <td>${rs.replicas}</td>
            <td>${rs.ready}</td>
            <td>${escapeHtml(rs.age)}</td>
          </tr>`).join('')}
        </tbody>
      </table></div>
    </div>`;

  const table = $('#bad-rs-table');
  makeSortable(table);
  const pag = attachPagination(table);

  function applyFilters() {
    const rows = $$('tbody tr', table);
    let visibleCount = 0;
    rows.forEach(row => {
      const ns = row.dataset.ns;
      const reason = row.dataset.reason;
      const show = selectedNamespaces.has(ns) && selectedReasons.has(reason);
      row.dataset.filtered = show ? '' : 'hide';
      row.style.display = show ? '' : 'none';
      if (show) visibleCount++;
    });
    for (const key of selectedRS) {
      const row = table.querySelector(`[data-rs-key="${CSS.escape(key)}"]`);
      if (row && row.closest('tr').dataset.filtered === 'hide') selectedRS.delete(key);
    }
    const countEl = document.getElementById('rs-visible-count');
    if (countEl) countEl.textContent = visibleCount;
    if (pag) pag.refresh();
    updatePurgeBtn();
    syncCheckboxUI();
  }

  function updatePurgeBtn() {
    const btn = $('#rs-purge-btn');
    if (btn) {
      const count = selectedRS.size;
      btn.textContent = count > 0 ? `Purge ${count} RS${count > 1 ? 's' : ''}` : 'Purge Selected';
      btn.disabled = count === 0;
    }
  }

  function syncCheckboxUI() {
    $$('.rs-check', table).forEach(cb => { cb.checked = selectedRS.has(cb.dataset.rsKey); });
    const selectAll = $('#rs-select-all');
    if (selectAll) {
      const visible = rsList.filter(r => selectedNamespaces.has(r.namespace) && selectedReasons.has(r.reason));
      selectAll.checked = visible.length > 0 && visible.every(r => selectedRS.has(rsKey(r)));
    }
  }

  $('#rs-select-all')?.addEventListener('change', (e) => {
    const visible = rsList.filter(r => selectedNamespaces.has(r.namespace) && selectedReasons.has(r.reason));
    if (e.target.checked) visible.forEach(r => selectedRS.add(rsKey(r)));
    else visible.forEach(r => selectedRS.delete(rsKey(r)));
    syncCheckboxUI();
    updatePurgeBtn();
  });

  table.addEventListener('change', (e) => {
    if (!e.target.classList.contains('rs-check')) return;
    const key = e.target.dataset.rsKey;
    if (e.target.checked) selectedRS.add(key); else selectedRS.delete(key);
    const selectAll = $('#rs-select-all');
    if (selectAll) {
      const visible = rsList.filter(r => selectedNamespaces.has(r.namespace) && selectedReasons.has(r.reason));
      selectAll.checked = visible.length > 0 && visible.every(r => selectedRS.has(rsKey(r)));
    }
    updatePurgeBtn();
  });

  $('#rs-purge-btn')?.addEventListener('click', () => {
    if (selectedRS.size === 0) return;
    const keyToRS = new Map();
    rsList.forEach(r => keyToRS.set(rsKey(r), r));
    const selected = [];
    for (const key of selectedRS) { if (keyToRS.has(key)) selected.push(keyToRS.get(key)); }
    if (selected.length === 0) return;

    const nsSummary = {};
    selected.forEach(r => { nsSummary[r.namespace] = (nsSummary[r.namespace] || 0) + 1; });
    const detail = Object.entries(nsSummary).map(([ns, c]) => `${ns}: ${c}`).join(', ');
    confirmDialog(
      `Delete <strong>${selected.length}</strong> ReplicaSet${selected.length > 1 ? 's' : ''}?<br><br>${detail}`,
      async () => {
        const overlay = document.createElement('div');
        overlay.className = 'modal-overlay';
        overlay.style.cssText = 'cursor:wait;';
        overlay.innerHTML = `<div class="modal" style="text-align:center;min-width:320px">
          <div class="loading" style="padding:24px 0;font-size:14px;font-weight:500">Deleting ${selected.length} ReplicaSet${selected.length > 1 ? 's' : ''}...</div>
        </div>`;
        document.body.appendChild(overlay);

        try {
          const allRS = selected.map(r => ({ name: r.name, namespace: r.namespace }));
          const BATCH = 500;
          let totalDeleted = 0;
          let allErrors = [];
          for (let i = 0; i < allRS.length; i += BATCH) {
            const batch = allRS.slice(i, i + BATCH);
            const res = await apiPost('/actions/delete-replicasets', { replicaSets: batch });
            totalDeleted += res.deleted || 0;
            if (res.errors) allErrors = allErrors.concat(res.errors);
          }
          overlay.remove();
          showPurgeResult(totalDeleted, allErrors, () => renderBadReplicaSets(contentEl));
        } catch (err) {
          overlay.remove();
          toast('Delete failed: ' + err.message, 'error');
        }
      }
    );
  });

  function syncNsCheckboxes() { $$('#rs-ns-checks input').forEach(cb => { cb.checked = selectedNamespaces.has(cb.dataset.ns); }); }
  $('#rs-ns-all')?.addEventListener('click', () => { selectedNamespaces = new Set(namespaces); syncNsCheckboxes(); applyFilters(); });
  $('#rs-ns-none')?.addEventListener('click', () => { selectedNamespaces = new Set(); syncNsCheckboxes(); applyFilters(); });
  $$('#rs-ns-checks input').forEach(cb => {
    cb.addEventListener('change', () => {
      if (cb.checked) selectedNamespaces.add(cb.dataset.ns); else selectedNamespaces.delete(cb.dataset.ns);
      applyFilters();
    });
  });

  function syncReasonCheckboxes() { $$('#rs-reason-checks input').forEach(cb => { cb.checked = selectedReasons.has(cb.dataset.reason); }); }
  $('#rs-reason-all')?.addEventListener('click', () => { selectedReasons = new Set(reasons); syncReasonCheckboxes(); applyFilters(); });
  $('#rs-reason-none')?.addEventListener('click', () => { selectedReasons = new Set(); syncReasonCheckboxes(); applyFilters(); });
  $$('#rs-reason-checks input').forEach(cb => {
    cb.addEventListener('change', () => {
      if (cb.checked) selectedReasons.add(cb.dataset.reason); else selectedReasons.delete(cb.dataset.reason);
      applyFilters();
    });
  });
}

// ─── Shared: Purge result modal ─────────────────────────────────────────
function showPurgeResult(deleted, allErrors, onDone) {
  const errCount = allErrors.length;
  const resultOverlay = document.createElement('div');
  resultOverlay.className = 'modal-overlay';
  const errDetail = errCount > 0
    ? `<div style="margin-top:12px;font-size:12px;color:var(--red)">${allErrors.map(e => `<div>${escapeHtml(e.namespace)}/${escapeHtml(e.name)}: ${escapeHtml(e.error)}</div>`).join('')}</div>`
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
  resultOverlay.querySelector('#purge-done-btn').onclick = () => { resultOverlay.remove(); onDone(); };
  resultOverlay.onclick = (e) => { if (e.target === resultOverlay) { resultOverlay.remove(); onDone(); } };
}
