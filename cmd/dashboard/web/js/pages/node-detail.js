import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, utilBar, badge, errorMsg } from '../utils.js';
import { skeleton, breadcrumbs, makeSortable } from '../components.js';
import { makeChart } from '../charts.js';

const container = () => $('#page-container');

export async function renderNodeDetail(params) {
  const name = params.name;
  container().innerHTML = skeleton(5);
  try {
    const data = await api(`/nodes/${encodeURIComponent(name)}`);
    const node = data.node || data;
    const pods = toArray(data.pods || data.podList, 'pods');
    const disks = node.disks || [];
    const totalDiskGiB = disks.reduce((sum, d) => sum + (d.sizeGiB || 0), 0);

    container().innerHTML = `
      ${breadcrumbs([
        { label: 'Nodes', href: '#/nodes' },
        { label: name }
      ])}
      <div class="page-header"><h1>${name}</h1><p>Node detail view</p></div>
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Instance Type</div><div class="value">${node.instanceType || ''}</div></div>
        <div class="kpi-card"><div class="label">Node Group</div><div class="value"><a href="#/nodegroups/${node.nodeGroupId || node.nodeGroup || ''}" class="link">${node.nodeGroup || ''}</a></div></div>
        <div class="kpi-card"><div class="label">CPU Utilization</div><div class="value">${fmtPct(node.cpuUtilPct)}</div></div>
        <div class="kpi-card"><div class="label">Memory Utilization</div><div class="value">${fmtPct(node.memUtilPct)}</div></div>
        <div class="kpi-card"><div class="label">Pod Count</div><div class="value blue">${node.podCount ?? pods.length}</div></div>
        <div class="kpi-card"><div class="label">Hourly Cost</div><div class="value">${fmt$(node.hourlyCostUSD)}</div></div>
        <div class="kpi-card"><div class="label">Spot</div><div class="value">${node.isSpot ? badge('Spot', 'blue') : badge('On-Demand', 'gray')}</div></div>
      </div>
      ${disks.length ? `<div class="card">
        <h2>Attached disks <span style="font-weight:400;font-size:12px;color:var(--text-muted)">${disks.length} disk${disks.length > 1 ? 's' : ''} &middot; ${totalDiskGiB} GiB total</span></h2>
        <div class="table-wrap"><table>
          <thead><tr><th>Device</th><th>Type</th><th>Size</th><th>IOPS</th><th>Throughput</th><th>Encrypted</th></tr></thead>
          <tbody>${disks.map(d => `<tr>
            <td><code style="font-size:12px;background:var(--bg);padding:2px 6px;border-radius:4px">${d.name || ''}</code></td>
            <td>${badge(d.type || 'unknown', d.type === 'io2' ? 'purple' : 'blue')}</td>
            <td><strong>${d.sizeGiB || 0} GiB</strong></td>
            <td>${d.iops ? d.iops.toLocaleString() : '-'}</td>
            <td>${d.throughputMBps ? d.throughputMBps + ' MB/s' : '-'}</td>
            <td>${d.encrypted ? badge('Yes', 'green') : badge('No', 'gray')}</td>
          </tr>`).join('')}</tbody>
        </table></div>
      </div>` : ''}
      <div class="grid-2">
        <div class="card">
          <h2>CPU Usage</h2>
          <div class="chart-container"><canvas id="node-cpu-chart"></canvas></div>
        </div>
        <div class="card">
          <h2>Memory Usage</h2>
          <div class="chart-container"><canvas id="node-mem-chart"></canvas></div>
        </div>
      </div>
      <div class="card">
        <h2>Pods on this Node</h2>
        <div class="table-wrap"><table id="node-pods-table">
          <thead><tr><th>Name</th><th>Namespace</th><th>CPU Request</th><th>Memory Request</th><th>Status</th></tr></thead>
          <tbody id="node-pods-body"></tbody>
        </table></div>
      </div>`;

    // Resource charts
    const cpuUsed = parseFloat(node.cpuUsed) || 0;
    const cpuCap = parseFloat(node.cpuCapacity) || 1;
    makeChart('node-cpu-chart', {
      type: 'bar',
      data: {
        labels: ['CPU'],
        datasets: [
          { label: 'Used', data: [cpuUsed], backgroundColor: '#4361ee', borderRadius: 4 },
          { label: 'Capacity', data: [cpuCap - cpuUsed], backgroundColor: '#e2e8f0', borderRadius: 4 },
        ]
      },
      options: { responsive: true, maintainAspectRatio: false, indexAxis: 'y', plugins: { legend: { position: 'bottom' } }, scales: { x: { stacked: true, beginAtZero: true }, y: { stacked: true } } }
    });

    const memUsed = parseFloat(node.memUsed) || 0;
    const memCap = parseFloat(node.memCapacity) || 1;
    makeChart('node-mem-chart', {
      type: 'bar',
      data: {
        labels: ['Memory'],
        datasets: [
          { label: 'Used', data: [memUsed], backgroundColor: '#8b5cf6', borderRadius: 4 },
          { label: 'Capacity', data: [memCap - memUsed], backgroundColor: '#e2e8f0', borderRadius: 4 },
        ]
      },
      options: { responsive: true, maintainAspectRatio: false, indexAxis: 'y', plugins: { legend: { position: 'bottom' } }, scales: { x: { stacked: true, beginAtZero: true }, y: { stacked: true } } }
    });

    // Pods table
    $('#node-pods-body').innerHTML = pods.length ? pods.map(p => `<tr>
      <td>${p.name || ''}</td><td>${p.namespace || ''}</td>
      <td>${p.cpuRequest || '0m'}</td><td>${p.memRequest || '0Mi'}</td>
      <td>${badge(p.status || 'Running', p.status === 'Running' ? 'green' : 'amber')}</td>
    </tr>`).join('') : '<tr><td colspan="5" style="color:var(--text-muted)">No pods found</td></tr>';
    makeSortable($('#node-pods-table'));

  } catch (e) {
    container().innerHTML = errorMsg(`Failed to load node ${name}: ${e.message}`);
  }
}
