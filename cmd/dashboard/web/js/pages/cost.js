import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, errorMsg } from '../utils.js';
import { makeBarChart, makeAreaChart, makeDonutChart, destroyCharts } from '../charts.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, cardHeader, dateRangePicker, badge, exportCSV } from '../components.js';
import { renderSavings } from './savings.js';

const container = () => $('#page-container');

const tabDefs = [
  { id: 'dashboard', label: 'Dashboard' },
  { id: 'savings', label: 'Savings' },
  { id: 'namespace', label: 'Namespace' },
  { id: 'workload', label: 'Workload' },
];

const subRenderers = {
  savings: renderSavings,
  namespace: renderNamespaceBreakdown,
  workload: renderWorkloadBreakdown,
};

export async function renderCost(params) {
  const activeTab = params?.tab || 'dashboard';

  container().innerHTML = `
    <div class="page-header"><h1>Cost</h1><p>Cost analysis and savings opportunities</p></div>
    <div class="tabs" id="cost-tabs">
      ${tabDefs.map(t => `<button class="tab ${t.id === activeTab ? 'tab-active' : ''}" data-tab="${t.id}">${t.label}</button>`).join('')}
    </div>
    <div id="cost-content"></div>`;

  const contentEl = document.getElementById('cost-content');

  async function switchTab(tabId) {
    destroyCharts();
    contentEl.innerHTML = '';
    if (tabId === 'dashboard') {
      await renderCostDashboard(contentEl);
    } else {
      const render = subRenderers[tabId];
      if (render) await render(contentEl);
    }
  }

  // Tab click handlers
  document.getElementById('cost-tabs').addEventListener('click', (e) => {
    const btn = e.target.closest('.tab');
    if (!btn) return;
    const tabId = btn.dataset.tab;
    document.querySelectorAll('#cost-tabs .tab').forEach(b => b.classList.remove('tab-active'));
    btn.classList.add('tab-active');
    history.replaceState(null, '', tabId === 'dashboard' ? '#/cost' : `#/cost/${tabId}`);
    switchTab(tabId);
  });

  await switchTab(activeTab);
}

async function renderCostDashboard(targetEl) {
  targetEl.innerHTML = skeleton(5);
  try {
    const [summary, byNs, byWl, trend, savings, comparison] = await Promise.all([
      api('/cost/summary'), api('/cost/by-namespace'), api('/cost/by-workload').catch(() => null),
      api('/cost/trend').catch(() => null), api('/cost/savings').catch(() => null),
      api('/cost/comparison').catch(() => null),
    ]);
    const cs = summary || {};
    const savingsList = savings ? toArray(savings, 'opportunities', 'savings') : [];
    const totalIdentified = savingsList.reduce((s, r) => s + (r.estimatedSavings || r.savings || 0), 0);

    const nsMap = (byNs && typeof byNs === 'object' && !Array.isArray(byNs)) ? byNs : {};
    const nsEntries = Object.entries(nsMap).filter(([_, v]) => typeof v === 'number').sort((a, b) => b[1] - a[1]);
    const topNs = nsEntries.slice(0, 8);

    const trendPoints = trend ? toArray(trend, 'dataPoints', 'points') : [];

    targetEl.innerHTML = `
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total cost (MTD)</div><div class="value">${fmt$(cs.totalMonthlyCostUSD)}</div></div>
        <div class="kpi-card"><div class="label">Projected monthly</div><div class="value blue">${fmt$(cs.projectedMonthlyCostUSD)}</div></div>
        <div class="kpi-card"><div class="label">Nodes</div><div class="value">${cs.nodeCount || 0}</div></div>
        <div class="kpi-card"><div class="label">Potential savings</div><div class="value green">${fmt$(cs.potentialSavings)}</div></div>
      </div>
      <div class="card">
        <h2>Cost by namespace</h2>
        <div class="chart-container" id="ns-cost-chart"></div>
      </div>
      <div class="card">
        <div class="card-header"><h2>Cost trend (30d)</h2><div class="card-header-actions">${dateRangePicker('30d')}</div></div>
        <div class="chart-container" id="cost-trend-chart"></div>
      </div>
      ${comparison ? `<div class="card">
        <h2>Month-over-month</h2>
        <div class="grid-2">
          <div class="chart-container" id="mom-chart"></div>
          <div>
            <div class="table-wrap"><table id="mom-table">
              <thead><tr><th>Namespace</th><th>${comparison.previousPeriod || 'Last month'}</th><th>${comparison.currentPeriod || 'This month'}</th><th>Change</th></tr></thead>
              <tbody id="mom-body"></tbody>
            </table></div>
          </div>
        </div>
      </div>` : ''}
      ${savingsList.length ? `<div class="card">
        ${cardHeader('Savings opportunities', '<button class="btn btn-gray btn-sm" onclick="window.__exportSavingsCSV()">Export CSV</button>')}
        <div class="table-wrap"><table id="savings-table"><thead><tr><th>Type</th><th>Description</th><th>Est. savings</th></tr></thead><tbody id="savings-body"></tbody></table></div>
      </div>` : ''}
      <div class="card">
        ${cardHeader('Cost by workload', '<button class="btn btn-gray btn-sm" onclick="window.__exportWlCostCSV()">Export CSV</button>')}
        ${filterBar({ placeholder: 'Search workloads...', filters: [] })}
        <div class="table-wrap"><table id="wl-cost-table">
          <thead><tr><th>Namespace</th><th>Kind</th><th>Name</th><th>Monthly cost</th></tr></thead>
          <tbody id="wl-cost-body"></tbody>
        </table></div>
      </div>`;

    // Namespace cost - donut chart
    if (topNs.length) {
      const nsColors = ['#6366f1', '#8b5cf6', '#a78bfa', '#06b6d4', '#14b8a6', '#10b981', '#f59e0b', '#64748b'];
      makeDonutChart('ns-cost-chart', {
        labels: topNs.map(([k]) => k),
        series: topNs.map(([_, v]) => v),
        colors: nsColors.slice(0, topNs.length),
      });
    }

    // Cost trend - area chart with gradient fill
    if (trendPoints.length) {
      makeAreaChart('cost-trend-chart', {
        categories: trendPoints.map(p => p.date || p.timestamp || ''),
        series: [{ name: 'Daily cost', data: trendPoints.map(p => p.cost || p.value || 0) }],
        colors: ['#6366f1'],
      });
    }

    // Month-over-Month comparison - grouped bar
    if (comparison) {
      const cur = comparison.current || {};
      const prev = comparison.previous || {};
      const cats = ['computeCost', 'storageCost', 'networkCost', 'otherCost'];
      const catLabels = ['Compute', 'Storage', 'Network', 'Other'];
      makeBarChart('mom-chart', {
        categories: catLabels,
        series: [
          { name: comparison.previousPeriod || 'Last month', data: cats.map(c => prev[c] || 0) },
          { name: comparison.currentPeriod || 'This month', data: cats.map(c => cur[c] || 0) },
        ],
        colors: ['#475569', '#6366f1'],
      });
      const byNsComp = comparison.byNamespace || [];
      const momBody = $('#mom-body');
      if (momBody) {
        momBody.innerHTML = byNsComp.map(n => {
          const change = n.change || 0;
          const changeClass = change < 0 ? 'green' : change > 0 ? 'red' : '';
          const arrow = change < 0 ? '\u2193' : change > 0 ? '\u2191' : '';
          return `<tr>
            <td><strong>${n.namespace || ''}</strong></td>
            <td>${fmt$(n.previousCost)}</td>
            <td>${fmt$(n.currentCost)}</td>
            <td class="${changeClass}">${arrow} ${fmtPct(Math.abs(change))}</td>
          </tr>`;
        }).join('');
        makeSortable($('#mom-table'));
      }
    }

    // Savings table
    if (savingsList.length) {
      const sb = $('#savings-body');
      if (sb) sb.innerHTML = savingsList.map(s => `<tr class="savings-row">
        <td>${badge(s.type || 'optimization', 'green')}</td>
        <td>${s.description || s.name || ''}</td>
        <td class="value green">${fmt$(s.estimatedSavings || s.savings)}</td>
      </tr>`).join('');
      makeSortable($('#savings-table'));
    }

    // Workload cost table
    const wlList = toArray(byWl, 'workloads');
    $('#wl-cost-body').innerHTML = wlList.length ? wlList.map(w => `<tr class="clickable-row" onclick="location.hash='#/workloads/${w.namespace}/${w.kind}/${w.name}'">
      <td>${w.namespace || ''}</td><td>${w.kind || ''}</td><td>${w.name || ''}</td>
      <td><strong>${fmt$(w.monthlyCostUSD)}</strong></td>
    </tr>`).join('') : '<tr><td colspan="4" style="color:var(--text-muted)">No workload cost data</td></tr>';
    makeSortable($('#wl-cost-table'));

    // Attach filter
    const fb = targetEl.querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#wl-cost-table'));

    // CSV exports
    window.__exportSavingsCSV = () => {
      exportCSV(['Type', 'Description', 'Est. Savings'],
        savingsList.map(s => [s.type, s.description || s.name, s.estimatedSavings || s.savings]),
        'koptimizer-savings.csv');
    };
    window.__exportWlCostCSV = () => {
      exportCSV(['Namespace', 'Kind', 'Name', 'Monthly Cost'],
        wlList.map(w => [w.namespace, w.kind, w.name, w.monthlyCostUSD]),
        'koptimizer-workload-costs.csv');
    };

  } catch (e) {
    targetEl.innerHTML = errorMsg('Failed to load cost data: ' + e.message);
  }
}

async function renderNamespaceBreakdown(targetEl) {
  targetEl.innerHTML = skeleton(3);
  try {
    const byNs = await api('/cost/by-namespace');
    const nsMap = (byNs && typeof byNs === 'object' && !Array.isArray(byNs)) ? byNs : {};
    const nsEntries = Object.entries(nsMap).filter(([_, v]) => typeof v === 'number').sort((a, b) => b[1] - a[1]);
    const total = nsEntries.reduce((s, [_, v]) => s + v, 0);

    targetEl.innerHTML = `
      <div class="kpi-grid" style="grid-template-columns:repeat(2,1fr)">
        <div class="kpi-card"><div class="label">Total Monthly Cost</div><div class="value">${fmt$(total)}</div></div>
        <div class="kpi-card"><div class="label">Namespaces</div><div class="value">${nsEntries.length}</div></div>
      </div>
      <div class="card">
        <h2>Cost by namespace</h2>
        <div class="chart-container" id="ns-breakdown-chart"></div>
      </div>
      <div class="card">
        ${cardHeader('Namespace Breakdown', '<button class="btn btn-gray btn-sm" onclick="window.__exportNsCSV()">Export CSV</button>')}
        <div class="table-wrap"><table id="ns-cost-table">
          <thead><tr><th>Namespace</th><th>Monthly Cost</th><th>% of Total</th></tr></thead>
          <tbody id="ns-cost-body"></tbody>
        </table></div>
      </div>`;

    if (nsEntries.length) {
      const topNs = nsEntries.slice(0, 8);
      const nsColors = ['#6366f1', '#8b5cf6', '#a78bfa', '#06b6d4', '#14b8a6', '#10b981', '#f59e0b', '#64748b'];
      makeDonutChart('ns-breakdown-chart', {
        labels: topNs.map(([k]) => k),
        series: topNs.map(([_, v]) => v),
        colors: nsColors.slice(0, topNs.length),
      });
    }

    $('#ns-cost-body').innerHTML = nsEntries.length ? nsEntries.map(([ns, cost]) => `<tr>
      <td><strong>${ns}</strong></td>
      <td>${fmt$(cost)}</td>
      <td>${fmtPct(total > 0 ? cost / total * 100 : 0)}</td>
    </tr>`).join('') : '<tr><td colspan="3" style="color:var(--text-muted)">No namespace cost data</td></tr>';
    makeSortable($('#ns-cost-table'));

    window.__exportNsCSV = () => {
      exportCSV(['Namespace', 'Monthly Cost', '% of Total'],
        nsEntries.map(([ns, cost]) => [ns, cost.toFixed(2), total > 0 ? (cost / total * 100).toFixed(1) : '0']),
        'koptimizer-namespace-costs.csv');
    };
  } catch (e) {
    targetEl.innerHTML = errorMsg('Failed to load namespace cost data: ' + e.message);
  }
}

async function renderWorkloadBreakdown(targetEl) {
  targetEl.innerHTML = skeleton(3);
  try {
    const byWl = await api('/cost/by-workload');
    const wlList = Array.isArray(byWl) ? byWl : (byWl && byWl.workloads ? byWl.workloads : []);
    wlList.sort((a, b) => (b.monthlyCostUSD || 0) - (a.monthlyCostUSD || 0));
    const total = wlList.reduce((s, w) => s + (w.monthlyCostUSD || 0), 0);

    targetEl.innerHTML = `
      <div class="kpi-grid" style="grid-template-columns:repeat(2,1fr)">
        <div class="kpi-card"><div class="label">Total Monthly Cost</div><div class="value">${fmt$(total)}</div></div>
        <div class="kpi-card"><div class="label">Workloads</div><div class="value">${wlList.length}</div></div>
      </div>
      <div class="card">
        ${cardHeader('Cost by Workload', '<button class="btn btn-gray btn-sm" onclick="window.__exportWlBreakdownCSV()">Export CSV</button>')}
        ${filterBar({ placeholder: 'Search workloads...', filters: [] })}
        <div class="table-wrap"><table id="wl-breakdown-table">
          <thead><tr><th>Namespace</th><th>Kind</th><th>Name</th><th>Monthly Cost</th><th>% of Total</th></tr></thead>
          <tbody id="wl-breakdown-body"></tbody>
        </table></div>
      </div>`;

    $('#wl-breakdown-body').innerHTML = wlList.length ? wlList.map(w => `<tr class="clickable-row" onclick="location.hash='#/workloads/${w.namespace}/${w.kind}/${w.name}'">
      <td>${w.namespace || ''}</td><td>${w.kind || ''}</td><td>${w.name || ''}</td>
      <td><strong>${fmt$(w.monthlyCostUSD)}</strong></td>
      <td>${fmtPct(total > 0 ? (w.monthlyCostUSD || 0) / total * 100 : 0)}</td>
    </tr>`).join('') : '<tr><td colspan="5" style="color:var(--text-muted)">No workload cost data</td></tr>';
    makeSortable($('#wl-breakdown-table'));

    const fb = targetEl.querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#wl-breakdown-table'));

    window.__exportWlBreakdownCSV = () => {
      exportCSV(['Namespace', 'Kind', 'Name', 'Monthly Cost', '% of Total'],
        wlList.map(w => [w.namespace, w.kind, w.name, (w.monthlyCostUSD || 0).toFixed(2), total > 0 ? ((w.monthlyCostUSD || 0) / total * 100).toFixed(1) : '0']),
        'koptimizer-workload-costs.csv');
    };
  } catch (e) {
    targetEl.innerHTML = errorMsg('Failed to load workload cost data: ' + e.message);
  }
}
