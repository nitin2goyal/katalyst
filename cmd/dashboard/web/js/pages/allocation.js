import { api } from '../api.js';
import { $, fmt$, fmtPct, errorMsg } from '../utils.js';
import { makeChart } from '../charts.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, cardHeader, exportCSV } from '../components.js';

export async function renderAllocation(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const [byNs, costSummary, byLabel] = await Promise.all([
      api('/cost/by-namespace'), api('/cost/summary'), api('/cost/by-label').catch(() => ({}))
    ]);
    const cs = costSummary || {};
    const totalCost = cs.totalMonthlyCostUSD || cs.projectedMonthlyCostUSD || 0;
    const nsMap = (byNs && typeof byNs === 'object' && !Array.isArray(byNs)) ? byNs : {};
    const nsEntries = Object.entries(nsMap).filter(([_, v]) => typeof v === 'number').sort((a, b) => b[1] - a[1]);
    const allocatedCost = nsEntries.reduce((s, [_, v]) => s + v, 0);
    const unallocated = Math.max(0, totalCost - allocatedCost);
    const allocatedPct = totalCost > 0 ? (allocatedCost / totalCost * 100) : 0;

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Allocation Groups</h1><p>Cost allocation and chargeback by namespace and labels</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total Cost (MTD)</div><div class="value">${fmt$(totalCost)}</div></div>
        <div class="kpi-card"><div class="label">Allocated</div><div class="value green">${fmtPct(allocatedPct)}</div><div class="sub">${fmt$(allocatedCost)}</div></div>
        <div class="kpi-card"><div class="label">Unallocated</div><div class="value amber">${fmtPct(100 - allocatedPct)}</div><div class="sub">${fmt$(unallocated)}</div></div>
        <div class="kpi-card"><div class="label">Namespaces</div><div class="value blue">${nsEntries.length}</div></div>
      </div>
      <div class="grid-2">
        <div class="card">
          <h2>Cost Distribution</h2>
          <div class="chart-container"><canvas id="alloc-pie-chart"></canvas></div>
        </div>
        <div class="card">
          <h2>Top Consumers</h2>
          <div class="chart-container"><canvas id="alloc-bar-chart"></canvas></div>
        </div>
      </div>
      <div class="card">
        ${cardHeader('Namespace Breakdown', '<button class="btn btn-gray btn-sm" onclick="window.__exportAllocCSV()">Export CSV</button>')}
        ${filterBar({ placeholder: 'Search namespaces...' })}
        <div class="table-wrap"><table id="alloc-table">
          <thead><tr><th>Namespace</th><th>Monthly Cost</th><th>% of Total</th><th>Cost Bar</th></tr></thead>
          <tbody id="alloc-body"></tbody>
        </table></div>
      </div>`;

    // Pie chart
    const colors = ['#4361ee', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#06b6d4', '#ec4899', '#84cc16'];
    if (nsEntries.length) {
      makeChart('alloc-pie-chart', {
        type: 'doughnut',
        data: {
          labels: nsEntries.map(([k]) => k),
          datasets: [{ data: nsEntries.map(([_, v]) => v), backgroundColor: nsEntries.map((_, i) => colors[i % colors.length]) }]
        },
        options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'right' } } }
      });
      makeChart('alloc-bar-chart', {
        type: 'bar',
        data: {
          labels: nsEntries.slice(0, 8).map(([k]) => k),
          datasets: [{ label: 'Cost ($)', data: nsEntries.slice(0, 8).map(([_, v]) => v), backgroundColor: '#4361ee', borderRadius: 4 }]
        },
        options: { responsive: true, maintainAspectRatio: false, indexAxis: 'y', plugins: { legend: { display: false } }, scales: { x: { beginAtZero: true } } }
      });
    }

    // Table
    const maxCost = nsEntries.length ? nsEntries[0][1] : 1;
    $('#alloc-body').innerHTML = nsEntries.length ? nsEntries.map(([ns, cost]) => {
      const pct = totalCost > 0 ? (cost / totalCost * 100) : 0;
      const barW = Math.max(2, cost / maxCost * 100);
      return `<tr>
        <td><strong>${ns}</strong></td>
        <td>${fmt$(cost)}</td>
        <td>${fmtPct(pct)}</td>
        <td><div style="background:rgba(67,97,238,0.15);border-radius:4px;height:8px;width:100%;max-width:200px"><div style="background:#4361ee;height:100%;border-radius:4px;width:${barW}%"></div></div></td>
      </tr>`;
    }).join('') : '<tr><td colspan="4" style="color:var(--text-muted)">No allocation data</td></tr>';
    makeSortable($('#alloc-table'));

    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#alloc-table'));

    window.__exportAllocCSV = () => {
      exportCSV(['Namespace', 'Monthly Cost', '% of Total'],
        nsEntries.map(([ns, cost]) => [ns, cost, totalCost > 0 ? (cost / totalCost * 100).toFixed(1) : 0]),
        'koptimizer-allocation.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load allocation data: ' + e.message);
  }
}
