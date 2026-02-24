import { api } from '../api.js';
import { $, fmt$, fmtPct, errorMsg, timeAgo } from '../utils.js';
import { skeleton, badge, cardHeader, exportCSV } from '../components.js';
import { makeChart } from '../charts.js';

export async function renderImpact(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const data = await api('/cost/impact');
    const summary = data?.summary || {};
    const monthly = data?.monthly || [];
    const byCategory = data?.byCategory || [];
    const recentActions = data?.recentActions || [];

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Impact Dashboard</h1><p>Track actual savings realized from optimization actions</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total Savings</div><div class="value green">${fmt$(summary.totalSavingsUSD)}</div><div class="sub">cumulative all-time</div></div>
        <div class="kpi-card"><div class="label">Avg Monthly Savings</div><div class="value green">${fmt$(summary.avgMonthlySavingsUSD)}</div><div class="sub">per month average</div></div>
        <div class="kpi-card"><div class="label">Actions Applied</div><div class="value blue">${summary.totalActionsApplied || 0}</div><div class="sub">optimization actions</div></div>
        <div class="kpi-card"><div class="label">Savings Capture Rate</div><div class="value ${(summary.savingsVsIdentifiedPct || 0) >= 70 ? 'green' : 'amber'}">${fmtPct(summary.savingsVsIdentifiedPct)}</div><div class="sub">of identified savings realized</div></div>
        <div class="kpi-card"><div class="label">ROI Multiple</div><div class="value green">${(summary.roiMultiple || 0).toFixed(1)}x</div><div class="sub">return on investment</div></div>
      </div>

      <div class="grid-2">
        <div class="card">
          <h2>Cumulative Savings Over Time</h2>
          <div class="chart-container"><canvas id="impact-cumulative-chart"></canvas></div>
        </div>
        <div class="card">
          <h2>Monthly Savings</h2>
          <div class="chart-container"><canvas id="impact-monthly-chart"></canvas></div>
        </div>
      </div>

      <div class="grid-2">
        <div class="card">
          <h2>Savings by Category</h2>
          <div class="chart-container"><canvas id="impact-category-chart"></canvas></div>
        </div>
        <div class="card">
          <h2>Category Breakdown</h2>
          <div class="detail-list" id="category-breakdown"></div>
        </div>
      </div>

      <div class="card">
        ${cardHeader('Recent Optimization Actions', '<button class="btn btn-gray btn-sm" onclick="window.__exportImpactCSV()">Export CSV</button>')}
        <div class="table-wrap"><table id="impact-actions-table">
          <thead><tr><th>Time</th><th>Action</th><th>Category</th><th>Savings</th></tr></thead>
          <tbody>${recentActions.length ? recentActions.map(a => `<tr>
            <td>${timeAgo(a.timestamp)}</td>
            <td>${a.action}</td>
            <td>${badge(a.category, 'blue')}</td>
            <td class="green">${fmt$(a.savingsUSD)}/mo</td>
          </tr>`).join('') : '<tr><td colspan="4" style="color:var(--text-muted)">No recent actions</td></tr>'}</tbody>
        </table></div>
      </div>`;

    // Cumulative savings line chart
    if (monthly.length) {
      makeChart('impact-cumulative-chart', {
        type: 'line',
        data: {
          labels: monthly.map(m => m.month),
          datasets: [{
            label: 'Cumulative Savings ($)',
            data: monthly.map(m => m.cumulativeSavings),
            borderColor: '#10b981',
            backgroundColor: 'rgba(16,185,129,0.1)',
            fill: true,
            tension: 0.3,
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          plugins: { legend: { display: false } },
          scales: {
            y: { beginAtZero: true, ticks: { callback: v => '$' + (v / 1000).toFixed(1) + 'k' } },
          },
        },
      });

      // Monthly savings bar chart
      makeChart('impact-monthly-chart', {
        type: 'bar',
        data: {
          labels: monthly.map(m => m.month),
          datasets: [{
            label: 'Monthly Savings ($)',
            data: monthly.map(m => m.savingsUSD),
            backgroundColor: '#4361ee',
            borderRadius: 4,
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          plugins: { legend: { display: false } },
          scales: {
            y: { beginAtZero: true, ticks: { callback: v => '$' + v } },
          },
        },
      });
    }

    // Category doughnut chart
    if (byCategory.length) {
      makeChart('impact-category-chart', {
        type: 'doughnut',
        data: {
          labels: byCategory.map(c => c.category),
          datasets: [{
            data: byCategory.map(c => c.savingsUSD),
            backgroundColor: byCategory.map(c => c.color),
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          plugins: {
            legend: { position: 'bottom' },
          },
        },
      });

      // Category breakdown detail
      const totalCatSavings = byCategory.reduce((s, c) => s + (c.savingsUSD || 0), 0);
      $('#category-breakdown').innerHTML = byCategory.map(c => {
        const pct = totalCatSavings > 0 ? ((c.savingsUSD / totalCatSavings) * 100).toFixed(1) : 0;
        return `<div class="detail-item">
          <span><span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:${c.color};margin-right:8px"></span>${c.category}</span>
          <span><strong>${fmt$(c.savingsUSD)}</strong> <span style="color:var(--text-muted);font-size:12px">(${pct}%) - ${c.actionsApplied} actions</span></span>
        </div>`;
      }).join('');
    }

    // CSV export
    window.__exportImpactCSV = () => {
      exportCSV(['Time', 'Action', 'Category', 'Monthly Savings USD'],
        recentActions.map(a => [a.timestamp, a.action, a.category, a.savingsUSD]),
        'koptimizer-impact-actions.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load impact data: ' + e.message);
  }
}
