import { api } from '../api.js';
import { $, fmt$, fmtPct, errorMsg, esc } from '../utils.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, attachPagination, exportCSV, cardHeader, badge } from '../components.js';

export async function renderHelmDrift() {
  const container = () => $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const data = await api('/helm-drift');

    if (!data || (!data.workloads?.length && !data.totalChecked)) {
      container().innerHTML = `
        <div class="page-header"><h1>Helm Drift</h1><p>Compare cluster workload config against Helm chart values</p></div>
        <div class="info-banner">
          Helm drift detection is not configured. Add <code>helmDrift</code> settings to your config with GitLab credentials and chart mappings.
        </div>`;
      return;
    }

    const wls = data.workloads || [];
    const drifted = wls.filter(w => w.fields?.some(f => f.drifted));

    container().innerHTML = `
      <div class="page-header"><h1>Helm Drift</h1><p>Deviations between cluster state and Helm chart values</p></div>
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Workloads Checked</div><div class="value blue">${data.totalChecked}</div></div>
        <div class="kpi-card"><div class="label">Drifted</div><div class="value ${data.totalDrifted > 0 ? 'red' : 'green'}">${data.totalDrifted}</div></div>
        <div class="kpi-card"><div class="label">Cost Impact</div><div class="value ${data.totalCostImpactUSD > 0 ? 'red' : 'green'}">${fmt$(data.totalCostImpactUSD)}</div><div class="sub">monthly over/under helm spec</div></div>
        <div class="kpi-card"><div class="label">Last Updated</div><div class="value" style="font-size:14px">${data.lastUpdated ? new Date(data.lastUpdated).toLocaleString() : '-'}</div></div>
      </div>
      <div class="card">
        ${cardHeader('Drift Analysis', `
          <button class="btn btn-gray btn-sm" onclick="window.__refreshDrift()">Refresh</button>
          <button class="btn btn-gray btn-sm" onclick="window.__exportDriftCSV()">Export CSV</button>
        `)}
        ${filterBar({ placeholder: 'Search workloads...' })}
        <div class="table-wrap"><table id="drift-table">
          <thead><tr>
            <th>Workload</th>
            <th>Namespace</th>
            <th>Replicas</th>
            <th>Field</th>
            <th>Helm (current)</th>
            <th>Helm (1w ago)</th>
            <th>Actual</th>
            <th>Status</th>
            <th>Cost Impact</th>
          </tr></thead>
          <tbody id="drift-body"></tbody>
        </table></div>
      </div>`;

    // Flatten: one row per drifted field
    const rows = [];
    for (const w of wls) {
      if (w.helmFetchError) {
        rows.push({ ...w, field: null, error: w.helmFetchError });
        continue;
      }
      const driftedFields = (w.fields || []).filter(f => f.drifted);
      if (driftedFields.length === 0) continue;
      for (const f of driftedFields) {
        rows.push({ ...w, field: f });
      }
    }

    const tbody = $('#drift-body');
    if (rows.length === 0) {
      tbody.innerHTML = '<tr><td colspan="9" style="color:var(--text-muted)">No drift detected — all workloads match Helm values</td></tr>';
    } else {
      tbody.innerHTML = rows.map(r => {
        if (r.error) {
          return `<tr>
            <td>${esc(r.chartPath || '')}</td>
            <td>-</td><td>-</td>
            <td colspan="5" style="color:var(--status-red)">${esc(r.error)}</td>
            <td>-</td>
          </tr>`;
        }
        const f = r.field;
        const costCls = f.costImpactUSD > 0 ? 'red' : f.costImpactUSD < 0 ? 'green' : '';
        return `<tr>
          <td><strong>${esc(r.name || '')}</strong><br><span style="font-size:11px;color:var(--text-muted)">${esc(r.kind || '')}</span></td>
          <td>${esc(r.namespace || '')}</td>
          <td>${r.replicas ?? '-'}</td>
          <td>${badge(f.field, 'blue')}</td>
          <td>${esc(f.helmValue)}</td>
          <td>${esc(f.helmWeekAgo)}</td>
          <td><strong>${esc(f.actualValue)}</strong></td>
          <td>${f.drifted ? badge('Drifted', 'red') : badge('OK', 'green')}</td>
          <td class="${costCls}">${f.costImpactUSD ? fmt$(f.costImpactUSD) + '/mo' : '-'}</td>
        </tr>`;
      }).join('');
    }

    const tableEl = $('#drift-table');
    makeSortable(tableEl);
    const pag = attachPagination(tableEl);
    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, tableEl, pag);

    // Refresh button
    window.__refreshDrift = async () => {
      container().innerHTML = skeleton(5);
      try {
        await api('/helm-drift?refresh=true');
        renderHelmDrift(targetEl);
      } catch (e) {
        container().innerHTML = errorMsg('Refresh failed: ' + e.message);
      }
    };

    // CSV export
    window.__exportDriftCSV = () => {
      exportCSV(
        ['Workload', 'Kind', 'Namespace', 'Replicas', 'Field', 'Helm Current', 'Helm 1w Ago', 'Actual', 'Drifted', 'Cost Impact'],
        rows.filter(r => !r.error).map(r => [
          r.name, r.kind, r.namespace, r.replicas,
          r.field.field, r.field.helmValue, r.field.helmWeekAgo, r.field.actualValue,
          r.field.drifted ? 'Yes' : 'No',
          r.field.costImpactUSD ? fmt$(r.field.costImpactUSD) : ''
        ]),
        'katalyst-helm-drift.csv'
      );
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load helm drift: ' + e.message);
  }
}
