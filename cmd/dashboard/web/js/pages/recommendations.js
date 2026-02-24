import { api, apiPost } from '../api.js';
import { $, toArray, fmt$, errorMsg } from '../utils.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, exportCSV, badge, cardHeader, toast } from '../components.js';

export async function renderRecsTab(targetEl) {
  targetEl.innerHTML = skeleton(5);
  try {
    const [recs] = await Promise.all([
      api('/recommendations'),
      api('/recommendations/summary').catch(() => null),
    ]);
    const recList = toArray(recs, 'recommendations');
    const pending = recList.filter(r => (r.status || r.Status) === 'pending').length;
    const approved = recList.filter(r => (r.status || r.Status) === 'approved').length;
    const dismissed = recList.filter(r => (r.status || r.Status) === 'dismissed').length;
    const totalSavings = recList.reduce((s, r) => s + (r.estimatedSavings || 0), 0);

    const types = [...new Set(recList.map(r => r.type || r.Type || r.category).filter(Boolean))];
    const statuses = ['pending', 'approved', 'dismissed'];

    targetEl.innerHTML = `
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total</div><div class="value">${recList.length}</div></div>
        <div class="kpi-card"><div class="label">Pending</div><div class="value amber">${pending}</div></div>
        <div class="kpi-card"><div class="label">Approved</div><div class="value green">${approved}</div></div>
        <div class="kpi-card"><div class="label">Est. Total Savings</div><div class="value green">${fmt$(totalSavings)}</div><div class="sub">if all pending approved</div></div>
      </div>
      <div class="card">
        ${cardHeader('All Recommendations', '<button class="btn btn-gray btn-sm" onclick="window.__exportRecsCSV()">Export CSV</button>')}
        ${filterBar({
          placeholder: 'Search recommendations...',
          filters: [
            { key: '0', label: 'Type', options: types },
            { key: '4', label: 'Status', options: statuses },
          ]
        })}
        <div class="table-wrap"><table id="rec-table">
          <thead><tr><th>Type</th><th>Target</th><th>Description</th><th>Savings</th><th>Confidence</th><th>Status</th><th>Actions</th></tr></thead>
          <tbody id="rec-body"></tbody>
        </table></div>
      </div>`;

    renderRecTable(recList);
    makeSortable($('#rec-table'));

    const fb = targetEl.querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#rec-table'));

    window.__exportRecsCSV = () => {
      exportCSV(['Type', 'Target', 'Description', 'Savings', 'Confidence %', 'Status'],
        recList.map(r => [r.type || r.Type, r.target || r.resource, r.description || r.summary, r.estimatedSavings, r.confidence ?? '', r.status || r.Status]),
        'koptimizer-recommendations.csv');
    };
  } catch (e) {
    targetEl.innerHTML = errorMsg('Failed to load recommendations: ' + e.message);
  }
}

function confBadge(conf) {
  if (conf == null) return '-';
  const cls = conf >= 85 ? 'green' : conf >= 65 ? 'amber' : 'red';
  return badge(conf + '%', cls);
}

function renderRecTable(recList) {
  $('#rec-body').innerHTML = recList.length ? recList.map(r => {
    const st = r.status || r.Status || 'unknown';
    const statusBadge = st === 'pending' ? badge('Pending', 'amber')
      : st === 'approved' ? badge('Approved', 'green')
      : st === 'dismissed' ? badge('Dismissed', 'gray')
      : badge(st, 'blue');
    const actions = st === 'pending' ? `
      <button class="btn btn-green btn-sm" onclick="event.stopPropagation();window.__approveRec('${r.id || r.ID}')">Approve</button>
      <button class="btn btn-gray btn-sm" onclick="event.stopPropagation();window.__dismissRec('${r.id || r.ID}')" style="margin-left:4px">Dismiss</button>
    ` : '';
    return `<tr>
      <td>${badge(r.type || r.Type || r.category || '', 'blue')}</td>
      <td>${r.target || r.resource || ''}</td>
      <td>${r.description || r.summary || ''}</td>
      <td class="value green">${fmt$(r.estimatedSavings)}</td>
      <td>${confBadge(r.confidence)}</td>
      <td>${statusBadge}</td>
      <td>${actions}</td>
    </tr>`;
  }).join('') : '<tr><td colspan="7" style="color:var(--text-muted)">No recommendations</td></tr>';
}

window.__approveRec = async function (id) {
  try {
    await apiPost(`/recommendations/${id}/approve`);
    toast('Recommendation approved', 'success');
    const el = document.getElementById('resources-content');
    if (el) await renderRecsTab(el);
  } catch (e) { toast('Failed to approve: ' + e.message, 'error'); }
};

window.__dismissRec = async function (id) {
  try {
    await apiPost(`/recommendations/${id}/dismiss`);
    toast('Recommendation dismissed', 'info');
    const el = document.getElementById('resources-content');
    if (el) await renderRecsTab(el);
  } catch (e) { toast('Failed to dismiss: ' + e.message, 'error'); }
};
