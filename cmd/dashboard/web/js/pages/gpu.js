import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, utilBar, errorMsg } from '../utils.js';
import { skeleton, makeSortable, exportCSV, cardHeader, badge } from '../components.js';

export async function renderGPU(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const [nodes, util, recs] = await Promise.all([
      api('/gpu/nodes'), api('/gpu/utilization').catch(() => null), api('/gpu/recommendations').catch(() => null),
    ]);
    const nodeList = toArray(nodes, 'nodes', 'gpuNodes');
    const utilData = util || {};
    const recList = toArray(recs, 'recommendations');

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>GPU Management</h1><p>GPU node utilization and optimization</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total GPUs</div><div class="value purple">${utilData.totalGPUs || nodeList.length}</div></div>
        <div class="kpi-card"><div class="label">Used GPUs</div><div class="value">${utilData.usedGPUs || 0}</div></div>
        <div class="kpi-card"><div class="label">GPU Utilization</div><div class="value">${fmtPct(utilData.utilizationPct)}</div></div>
        <div class="kpi-card"><div class="label">GPU Recommendations</div><div class="value">${recList.length}</div></div>
      </div>
      <div class="card">
        ${cardHeader('GPU Nodes', '<button class="btn btn-gray btn-sm" onclick="window.__exportGpuCSV()">Export CSV</button>')}
        <div class="table-wrap"><table id="gpu-table">
          <thead><tr><th>Name</th><th>Instance Type</th><th>GPUs</th><th>GPUs Used</th><th>CPU Util</th><th>Mem Util</th><th>Cost/hr</th></tr></thead>
          <tbody id="gpu-body"></tbody>
        </table></div>
      </div>
      ${recList.length ? `<div class="card"><h2>GPU Recommendations</h2>
        <div class="table-wrap"><table id="gpu-rec-table">
          <thead><tr><th>Type</th><th>Node</th><th>Description</th><th>Savings</th></tr></thead>
          <tbody id="gpu-rec-body"></tbody>
        </table></div></div>` : ''}`;

    $('#gpu-body').innerHTML = nodeList.length ? nodeList.map(n => `<tr class="clickable-row" onclick="location.hash='#/nodes/${n.name || ''}'">
      <td>${n.name || ''}</td><td>${n.instanceType || ''}</td>
      <td>${n.gpuCount ?? 0}</td><td>${n.gpuUsed ?? 0}</td>
      <td>${utilBar(n.cpuUtilPct)}</td><td>${utilBar(n.memUtilPct)}</td>
      <td>${fmt$(n.hourlyCostUSD)}</td>
    </tr>`).join('') : '<tr><td colspan="7" style="color:var(--text-muted)">No GPU nodes</td></tr>';

    if (recList.length) {
      $('#gpu-rec-body').innerHTML = recList.map(r => `<tr>
        <td>${badge(r.type || '', 'purple')}</td><td>${r.target || r.node || ''}</td>
        <td>${r.description || ''}</td><td class="value green">${fmt$(r.estimatedSavings)}</td>
      </tr>`).join('');
      makeSortable($('#gpu-rec-table'));
    }

    makeSortable($('#gpu-table'));

    window.__exportGpuCSV = () => {
      exportCSV(['Name', 'Instance Type', 'GPUs', 'GPUs Used', 'CPU Util %', 'Mem Util %', 'Cost/hr'],
        nodeList.map(n => [n.name, n.instanceType, n.gpuCount, n.gpuUsed, (n.cpuUtilPct||0).toFixed(1), (n.memUtilPct||0).toFixed(1), n.hourlyCostUSD]),
        'koptimizer-gpu-nodes.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load GPU data: ' + e.message);
  }
}
