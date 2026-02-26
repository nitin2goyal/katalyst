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
  // Selection state: tracks pod keys across all pages
  const selectedPods = new Set();

  function filteredPods() {
    return pods.filter(p => selectedNamespaces.has(p.namespace) && selectedStatuses.has(p.status));
  }

  function render() {
    const visible = filteredPods();
    // Build a lookup from podKey -> index in `visible` for checkbox data attributes
    const keyToIdx = new Map();
    visible.forEach((p, i) => keyToIdx.set(podKey(p), i));

    // Prune selectedPods: remove any that are no longer in the filtered set
    for (const key of selectedPods) {
      if (!keyToIdx.has(key)) selectedPods.delete(key);
    }

    const allSelected = visible.length > 0 && visible.every(p => selectedPods.has(podKey(p)));

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
                  <input type="checkbox" data-ns="${escapeHtml(ns)}" ${selectedNamespaces.has(ns) ? 'checked' : ''}>
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
                  <input type="checkbox" data-status="${escapeHtml(st)}" ${selectedStatuses.has(st) ? 'checked' : ''}>
                  ${badge(st, statusBadgeClass(st))} <span class="badge badge-gray">${byStatus[st]}</span>
                </label>
              `).join('')}
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        ${cardHeader(`Bad Pods (${visible.length})`,
          `<button class="btn btn-red btn-sm" id="purge-btn" ${selectedPods.size === 0 ? 'disabled' : ''}>` +
          (selectedPods.size > 0 ? `Purge ${selectedPods.size} Pod${selectedPods.size > 1 ? 's' : ''}` : 'Purge Selected Pods') +
          `</button>`
        )}
        <div class="table-wrap"><table id="bad-pods-table">
          <thead><tr>
            <th style="width:2rem"><input type="checkbox" id="select-all" ${allSelected ? 'checked' : ''}></th>
            <th>Name</th><th>Namespace</th><th>Status</th><th>Node</th><th>Age</th>
          </tr></thead>
          <tbody>
            ${visible.map((p, i) => `<tr data-idx="${i}">
              <td><input type="checkbox" class="pod-check" data-pod-key="${escapeHtml(podKey(p))}" ${selectedPods.has(podKey(p)) ? 'checked' : ''}></td>
              <td>${escapeHtml(p.name)}</td>
              <td>${escapeHtml(p.namespace)}</td>
              <td>${badge(p.status, statusBadgeClass(p.status))}</td>
              <td>${escapeHtml(p.node || '-')}</td>
              <td>${escapeHtml(p.age)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>
      </div>`;

    // Sortable + pagination
    const table = $('#bad-pods-table');
    makeSortable(table);
    attachPagination(table);

    function updatePurgeBtn() {
      const btn = $('#purge-btn');
      if (btn) {
        const count = selectedPods.size;
        btn.textContent = count > 0 ? `Purge ${count} Pod${count > 1 ? 's' : ''}` : 'Purge Selected Pods';
        btn.disabled = count === 0;
      }
    }

    function syncCheckboxUI() {
      // Sync visible checkboxes with selectedPods state
      $$('.pod-check', table).forEach(cb => {
        cb.checked = selectedPods.has(cb.dataset.podKey);
      });
      // Update select-all header checkbox
      const selectAll = $('#select-all');
      if (selectAll) {
        selectAll.checked = visible.length > 0 && visible.every(p => selectedPods.has(podKey(p)));
      }
    }

    // Select-all checkbox: toggles ALL filtered pods (across all pages)
    $('#select-all')?.addEventListener('change', (e) => {
      if (e.target.checked) {
        visible.forEach(p => selectedPods.add(podKey(p)));
      } else {
        visible.forEach(p => selectedPods.delete(podKey(p)));
      }
      syncCheckboxUI();
      updatePurgeBtn();
    });

    // Individual checkbox: update the selectedPods set
    table.addEventListener('change', (e) => {
      if (!e.target.classList.contains('pod-check')) return;
      const key = e.target.dataset.podKey;
      if (e.target.checked) {
        selectedPods.add(key);
      } else {
        selectedPods.delete(key);
      }
      // Update select-all state
      const selectAll = $('#select-all');
      if (selectAll) {
        selectAll.checked = visible.length > 0 && visible.every(p => selectedPods.has(podKey(p)));
      }
      updatePurgeBtn();
    });

    // Purge button
    $('#purge-btn')?.addEventListener('click', () => {
      if (selectedPods.size === 0) return;
      // Resolve selected keys back to pod objects
      const keyToPod = new Map();
      visible.forEach(p => keyToPod.set(podKey(p), p));
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
          try {
            const result = await apiPost('/actions/delete-pods', {
              pods: selected.map(p => ({ name: p.name, namespace: p.namespace })),
            });
            const errCount = (result.errors || []).length;
            if (errCount > 0) {
              toast(`Deleted ${result.deleted} pods, ${errCount} failed`, 'warning');
            } else {
              toast(`Deleted ${result.deleted} pods`, 'success');
            }
            // Refresh data
            renderActions(targetEl);
          } catch (err) {
            toast('Delete failed: ' + err.message, 'error');
          }
        }
      );
    });

    // Namespace filters
    $('#ns-all')?.addEventListener('click', () => { selectedNamespaces = new Set(namespaces); render(); });
    $('#ns-none')?.addEventListener('click', () => { selectedNamespaces = new Set(); render(); });
    $$('#ns-checks input').forEach(cb => {
      cb.addEventListener('change', () => {
        const ns = cb.dataset.ns;
        if (cb.checked) selectedNamespaces.add(ns); else selectedNamespaces.delete(ns);
        render();
      });
    });

    // Status filters
    $('#st-all')?.addEventListener('click', () => { selectedStatuses = new Set(statuses); render(); });
    $('#st-none')?.addEventListener('click', () => { selectedStatuses = new Set(); render(); });
    $$('#st-checks input').forEach(cb => {
      cb.addEventListener('change', () => {
        const st = cb.dataset.status;
        if (cb.checked) selectedStatuses.add(st); else selectedStatuses.delete(st);
        render();
      });
    });
  }

  render();
}
