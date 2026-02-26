import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, errorMsg } from '../utils.js';
import { skeleton, makeSortable, attachPagination, exportCSV, cardHeader, badge } from '../components.js';

export async function renderSpot(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(3);
  try {
    const [summary, nodes] = await Promise.all([
      api('/spot/summary').catch(() => null),
      api('/spot/nodes').catch(() => []),
    ]);
    const s = summary || {};
    const nodeList = Array.isArray(nodes) ? nodes : [];
    const spotNodes = nodeList.filter(n => n.lifecycle === 'spot');
    const odNodes = nodeList.filter(n => n.lifecycle !== 'spot');

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Spot Instances</h1><p>Spot vs on-demand instance analysis</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Spot Nodes</div><div class="value green">${s.spotNodes || 0}</div></div>
        <div class="kpi-card"><div class="label">On-Demand Nodes</div><div class="value">${s.onDemandNodes || 0}</div></div>
        <div class="kpi-card"><div class="label">Spot Adoption</div><div class="value">${fmtPct(s.spotPercentage)}</div></div>
        <div class="kpi-card"><div class="label">Est. Monthly Savings</div><div class="value green">${fmt$(s.estimatedMonthlySavingsUSD)}</div></div>
      </div>
      <div class="kpi-grid" style="grid-template-columns:repeat(2,1fr)">
        <div class="kpi-card"><div class="label">Spot Hourly Cost</div><div class="value">${fmt$(s.spotHourlyCostUSD)}/hr</div></div>
        <div class="kpi-card"><div class="label">On-Demand Hourly Cost</div><div class="value">${fmt$(s.onDemandHourlyCostUSD)}/hr</div></div>
      </div>
      <div class="card">
        ${cardHeader('All Nodes by Lifecycle', '<button class="btn btn-gray btn-sm" onclick="window.__exportSpotCSV()">Export CSV</button>')}
        <div class="table-wrap"><table id="spot-table">
          <thead><tr><th>Name</th><th>Instance Type</th><th>Lifecycle</th><th>Zone</th><th>Cost/hr</th></tr></thead>
          <tbody id="spot-body"></tbody>
        </table></div>
      </div>`;

    const tbody = $('#spot-body');
    if (tbody) {
      tbody.innerHTML = nodeList.length ? nodeList.map(n => `<tr class="clickable-row" onclick="location.hash='#/nodes/${n.name || ''}'">
        <td>${n.name || ''}</td><td>${n.instanceType || ''}</td>
        <td>${badge(n.lifecycle || 'on-demand', n.lifecycle === 'spot' ? 'green' : 'gray')}</td>
        <td>${n.zone || ''}</td>
        <td>${fmt$(n.hourlyCostUSD)}</td>
      </tr>`).join('') : '<tr><td colspan="5" style="color:var(--text-muted)">No nodes</td></tr>';
    }
    makeSortable($('#spot-table'));
    attachPagination($('#spot-table'));

    window.__exportSpotCSV = () => {
      exportCSV(['Name', 'Instance Type', 'Lifecycle', 'Zone', 'Cost/hr'],
        nodeList.map(n => [n.name, n.instanceType, n.lifecycle, n.zone, n.hourlyCostUSD]),
        'koptimizer-spot-nodes.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load spot data: ' + e.message);
  }
}
