import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, errorMsg } from '../utils.js';
import { makeChart } from '../charts.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, cardHeader, badge, exportCSV } from '../components.js';

export async function renderNetworkCost(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const data = await api('/cost/network').catch(() => null);
    const nc = data || {};
    const flows = toArray(nc, 'flows');
    const totalNetwork = nc.totalMonthlyCostUSD || 0;
    const crossAZ = nc.crossAZCostUSD || 0;
    const inAZ = nc.inAZCostUSD || 0;

    // Group by namespace
    const byNs = {};
    flows.forEach(f => {
      const ns = f.namespace || 'unknown';
      byNs[ns] = (byNs[ns] || 0) + (f.monthlyCostUSD || 0);
    });
    const nsEntries = Object.entries(byNs).sort((a, b) => b[1] - a[1]);

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Network Cost</h1><p>Cross-AZ and cross-region traffic cost analysis</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total Network Cost</div><div class="value">${fmt$(totalNetwork)}</div><div class="sub">monthly estimate</div></div>
        <div class="kpi-card"><div class="label">Cross-AZ Traffic</div><div class="value red">${fmt$(crossAZ)}</div><div class="sub">${totalNetwork > 0 ? fmtPct(crossAZ / totalNetwork * 100) : '0%'} of total</div></div>
        <div class="kpi-card"><div class="label">In-AZ Traffic</div><div class="value green">${fmt$(inAZ)}</div><div class="sub">no transfer cost</div></div>
        <div class="kpi-card"><div class="label">Top Talkers</div><div class="value blue">${flows.length}</div><div class="sub">tracked flows</div></div>
      </div>
      <div class="grid-2">
        <div class="card">
          <h2>Network Cost by Namespace</h2>
          <div class="chart-container"><canvas id="net-ns-chart"></canvas></div>
        </div>
        <div class="card">
          <h2>Cross-AZ vs In-AZ</h2>
          <div class="chart-container"><canvas id="net-az-chart"></canvas></div>
        </div>
      </div>
      <div class="card">
        ${cardHeader('Traffic Flows', '<button class="btn btn-gray btn-sm" onclick="window.__exportNetCSV()">Export CSV</button>')}
        ${filterBar({ placeholder: 'Search flows...', filters: [
          { key: '0', label: 'Namespace', options: [...new Set(flows.map(f => f.namespace).filter(Boolean))] }
        ] })}
        <div class="table-wrap"><table id="net-table">
          <thead><tr><th>Namespace</th><th>Workload</th><th>Source AZ</th><th>Dest AZ</th><th>Traffic</th><th>Monthly Cost</th><th>Type</th></tr></thead>
          <tbody id="net-body"></tbody>
        </table></div>
      </div>`;

    // Charts
    if (nsEntries.length) {
      makeChart('net-ns-chart', {
        type: 'bar',
        data: {
          labels: nsEntries.map(([k]) => k),
          datasets: [{ label: 'Network Cost ($)', data: nsEntries.map(([_, v]) => v), backgroundColor: '#4361ee', borderRadius: 4 }]
        },
        options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: { y: { beginAtZero: true } } }
      });
    }
    makeChart('net-az-chart', {
      type: 'doughnut',
      data: {
        labels: ['Cross-AZ', 'In-AZ'],
        datasets: [{ data: [crossAZ, inAZ], backgroundColor: ['#ef4444', '#10b981'] }]
      },
      options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'bottom' } } }
    });

    // Table
    $('#net-body').innerHTML = flows.length ? flows.map(f => {
      const isCross = f.sourceAZ !== f.destAZ;
      return `<tr${isCross ? ' class="warning-row"' : ''}>
        <td>${f.namespace || ''}</td>
        <td><strong>${f.workload || ''}</strong></td>
        <td>${f.sourceAZ || ''}</td>
        <td>${f.destAZ || ''}</td>
        <td>${f.trafficGB ? f.trafficGB.toFixed(1) + ' GB' : '-'}</td>
        <td>${fmt$(f.monthlyCostUSD)}</td>
        <td>${badge(isCross ? 'cross-az' : 'in-az', isCross ? 'red' : 'green')}</td>
      </tr>`;
    }).join('') : '<tr><td colspan="7" style="color:var(--text-muted)">No network flow data</td></tr>';
    makeSortable($('#net-table'));

    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#net-table'));

    window.__exportNetCSV = () => {
      exportCSV(['Namespace', 'Workload', 'Source AZ', 'Dest AZ', 'Traffic (GB)', 'Monthly Cost', 'Type'],
        flows.map(f => [f.namespace, f.workload, f.sourceAZ, f.destAZ, f.trafficGB, f.monthlyCostUSD, f.sourceAZ !== f.destAZ ? 'cross-az' : 'in-az']),
        'koptimizer-network-costs.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load network cost data: ' + e.message);
  }
}
