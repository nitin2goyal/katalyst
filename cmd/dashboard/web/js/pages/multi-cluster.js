import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, errorMsg } from '../utils.js';
import { makeChart } from '../charts.js';
import { skeleton, badge, toast, cardHeader } from '../components.js';

export async function renderMultiCluster(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const data = await api('/clusters').catch(() => null);
    const clusters = toArray(data, 'clusters');

    const totalCost = clusters.reduce((s, c) => s + (c.monthlyCostUSD || 0), 0);
    const totalNodes = clusters.reduce((s, c) => s + (c.nodeCount || 0), 0);
    const totalSavings = clusters.reduce((s, c) => s + (c.potentialSavings || 0), 0);

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Multi-Cluster Overview</h1><p>Organization-wide Kubernetes cost and efficiency</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Clusters</div><div class="value blue">${clusters.length}</div></div>
        <div class="kpi-card"><div class="label">Total Monthly Cost</div><div class="value">${fmt$(totalCost)}</div></div>
        <div class="kpi-card"><div class="label">Total Nodes</div><div class="value">${totalNodes}</div></div>
        <div class="kpi-card"><div class="label">Total Savings</div><div class="value green">${fmt$(totalSavings)}</div></div>
      </div>
      <div class="card">
        <h2>Cost by Cluster</h2>
        <div class="chart-container"><canvas id="mc-cost-chart"></canvas></div>
      </div>
      <div class="card">
        ${cardHeader('Clusters')}
        <div class="cluster-grid" id="cluster-grid"></div>
      </div>`;

    // Chart
    if (clusters.length) {
      makeChart('mc-cost-chart', {
        type: 'bar',
        data: {
          labels: clusters.map(c => c.name || ''),
          datasets: [
            { label: 'Monthly Cost', data: clusters.map(c => c.monthlyCostUSD || 0), backgroundColor: '#4361ee', borderRadius: 4 },
            { label: 'Potential Savings', data: clusters.map(c => c.potentialSavings || 0), backgroundColor: '#10b981', borderRadius: 4 }
          ]
        },
        options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'top' } }, scales: { y: { beginAtZero: true } } }
      });
    }

    // Cluster cards
    const providerBadge = p => {
      const cls = p === 'aws' ? 'amber' : p === 'gcp' ? 'blue' : p === 'azure' ? 'blue' : 'gray';
      return badge(p?.toUpperCase() || 'K8S', cls);
    };
    const effColor = s => s >= 80 ? 'green' : s >= 50 ? 'amber' : 'red';
    $('#cluster-grid').innerHTML = clusters.length ? clusters.map(c => `
      <div class="cluster-card" onclick="window.__switchCluster('${c.id || c.name}')">
        <div class="cluster-card-header">
          <span class="cluster-name">${c.name || ''}</span>
          ${providerBadge(c.provider)}
        </div>
        <div class="cluster-card-stats">
          <div class="cluster-stat"><span class="cluster-stat-label">Nodes</span><span class="cluster-stat-val">${c.nodeCount || 0}</span></div>
          <div class="cluster-stat"><span class="cluster-stat-label">Pods</span><span class="cluster-stat-val">${c.podCount || 0}</span></div>
          <div class="cluster-stat"><span class="cluster-stat-label">Cost/mo</span><span class="cluster-stat-val">${fmt$(c.monthlyCostUSD)}</span></div>
          <div class="cluster-stat"><span class="cluster-stat-label">Efficiency</span><span class="cluster-stat-val ${effColor(c.efficiencyScore)}">${c.efficiencyScore || 0}%</span></div>
        </div>
        <div class="cluster-card-savings">
          Potential savings: <strong class="green">${fmt$(c.potentialSavings)}</strong>
        </div>
      </div>
    `).join('') : '<div style="color:var(--text-muted);padding:24px;text-align:center">No clusters registered</div>';

    window.__switchCluster = (id) => {
      toast('Switched to cluster: ' + id, 'info');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load cluster data: ' + e.message);
  }
}
