import { api } from '../api.js';
import { $, toArray, fmt$, utilBar, errorMsg } from '../utils.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, exportCSV, cardHeader, badge } from '../components.js';

export async function renderCommitments(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const [all, underutil, expiring] = await Promise.all([
      api('/commitments'), api('/commitments/underutilized').catch(() => null), api('/commitments/expiring').catch(() => null),
    ]);
    const cList = toArray(all, 'commitments');
    const underList = toArray(underutil, 'commitments');
    const expList = toArray(expiring, 'commitments');
    const underIds = new Set(underList.map(c => c.id));
    const expIds = new Set(expList.map(c => c.id));

    const types = [...new Set(cList.map(c => c.type).filter(Boolean))];
    const utilLevels = ['High (>80%)', 'Medium (50-80%)', 'Low (<50%)'];

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Commitments</h1><p>Reserved instances and savings plans</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total Commitments</div><div class="value">${cList.length}</div></div>
        <div class="kpi-card"><div class="label">Underutilized</div><div class="value ${underList.length ? 'amber' : ''}">${underList.length}</div></div>
        <div class="kpi-card"><div class="label">Expiring Soon</div><div class="value ${expList.length ? 'red' : ''}">${expList.length}</div></div>
      </div>
      <div class="card">
        ${cardHeader('All Commitments', '<button class="btn btn-gray btn-sm" onclick="window.__exportCommitmentsCSV()">Export CSV</button>')}
        ${filterBar({
          placeholder: 'Search commitments...',
          filters: [
            { key: '1', label: 'Type', options: types },
          ]
        })}
        <div class="table-wrap"><table id="commit-table">
          <thead><tr><th>ID</th><th>Type</th><th>Instance Type</th><th>Utilization</th><th>Expires</th><th>Cost/hr</th><th>Flags</th></tr></thead>
          <tbody id="commit-body"></tbody>
        </table></div>
      </div>`;

    $('#commit-body').innerHTML = cList.length ? cList.map(c => {
      const isUnder = underIds.has(c.id);
      const isExp = expIds.has(c.id);
      const rowClass = isUnder ? 'warning-row' : isExp ? 'savings-row' : '';
      const flags = [
        isUnder ? badge('Underutilized', 'amber') : '',
        isExp ? badge('Expiring', 'red') : '',
      ].filter(Boolean).join(' ') || '-';
      return `<tr class="${rowClass}">
        <td>${c.id || ''}</td><td>${c.type || ''}</td><td>${c.instanceType || ''}</td>
        <td>${utilBar(c.utilizationPct)}</td>
        <td>${c.expiresAt || ''}</td><td>${fmt$(c.hourlyCostUSD)}</td>
        <td>${flags}</td>
      </tr>`;
    }).join('') : '<tr><td colspan="7" style="color:var(--text-muted)">No commitments found. GCP Committed Use Discounts (CUDs) are managed via the GCP Console. Configure the CUD integration in Settings to import commitment data.</td></tr>';

    makeSortable($('#commit-table'));

    // Attach filter
    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#commit-table'));

    // CSV export
    window.__exportCommitmentsCSV = () => {
      exportCSV(['ID', 'Type', 'Instance Type', 'Utilization %', 'Expires', 'Cost/hr'],
        cList.map(c => [c.id, c.type, c.instanceType, (c.utilizationPct||0).toFixed(1), c.expiresAt, c.hourlyCostUSD]),
        'koptimizer-commitments.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load commitment data: ' + e.message);
  }
}
