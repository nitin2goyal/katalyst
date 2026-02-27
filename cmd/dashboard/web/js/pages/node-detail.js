import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, fmtCPU, fmtMem, utilBar, badge, errorMsg } from '../utils.js';
import { skeleton, breadcrumbs, makeSortable } from '../components.js';
import { makeBarChart } from '../charts.js';

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
        <div class="kpi-card"><div class="label">Pod Count</div><div class="value blue">${node.appPodCount != null ? node.appPodCount + ' <span style="font-size:0.6em;color:var(--text-muted)">+ ' + (node.systemPodCount || 0) + ' sys</span>' : (node.podCount ?? pods.length)}</div></div>
        <div class="kpi-card"><div class="label">Boot Disk</div><div class="value" style="font-size:0.9rem">${node.diskType ? (node.diskType.replace(/^pd-/, 'PD ').replace(/^hyperdisk-/, 'Hyperdisk ').replace(/\b\w/g, c => c.toUpperCase())) + (node.diskSizeGB ? ' ' + node.diskSizeGB + 'G' : '') : '-'}</div></div>
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
          <div class="chart-container" id="node-cpu-chart"></div>
        </div>
        <div class="card">
          <h2>Memory Usage</h2>
          <div class="chart-container" id="node-mem-chart"></div>
        </div>
      </div>
      <div class="card">
        <h2>Pods on this Node</h2>
        <div class="table-wrap"><table id="node-pods-table">
          <thead><tr><th>Name</th><th>Namespace</th><th>Type</th><th>CPU Request</th><th>Memory Request</th><th>Status</th></tr></thead>
          <tbody id="node-pods-body"></tbody>
        </table></div>
      </div>`;

    // Resource charts — values are in millicores (CPU) and bytes (Memory)
    const cpuUsedMilli = parseFloat(node.cpuUsed) || 0;
    const cpuCapMilli = parseFloat(node.cpuCapacity) || 1;
    const cpuUsedCores = cpuUsedMilli / 1000;
    const cpuCapCores = cpuCapMilli / 1000;
    makeBarChart('node-cpu-chart', {
      categories: ['CPU (cores)'],
      series: [
        { name: 'Used', data: [parseFloat(cpuUsedCores.toFixed(1))] },
        { name: 'Available', data: [parseFloat((cpuCapCores - cpuUsedCores).toFixed(1))] },
      ],
      colors: ['#4361ee', '#e2e8f0'],
      horizontal: true,
      stacked: true,
      noCurrency: true,
    });

    const memUsedBytes = parseFloat(node.memUsed) || 0;
    const memCapBytes = parseFloat(node.memCapacity) || 1;
    const memUsedGB = memUsedBytes / 1073741824;
    const memCapGB = memCapBytes / 1073741824;
    makeBarChart('node-mem-chart', {
      categories: ['Memory (GB)'],
      series: [
        { name: 'Used', data: [parseFloat(memUsedGB.toFixed(1))] },
        { name: 'Available', data: [parseFloat((memCapGB - memUsedGB).toFixed(1))] },
      ],
      colors: ['#8b5cf6', '#e2e8f0'],
      horizontal: true,
      stacked: true,
      noCurrency: true,
    });

    // Use shared fmtCPU / fmtMem from utils.js

    // Pods table — color-code unhealthy statuses
    const podStatusColor = (s) => {
      if (!s) return 'gray';
      const lower = s.toLowerCase();
      if (lower === 'running' || lower === 'succeeded' || lower === 'completed') return 'green';
      if (lower === 'pending' || lower === 'containercreating' || lower === 'podinitializing') return 'blue';
      if (lower.includes('backoff') || lower.includes('error') || lower.includes('oomkilled') || lower === 'failed') return 'red';
      if (lower.includes('pull') || lower.includes('terminating')) return 'amber';
      return 'amber';
    };
    const podStatusReason = (s) => {
      if (!s) return '';
      const reasons = {
        'running': 'Pod is running normally',
        'succeeded': 'Pod completed successfully',
        'completed': 'Pod completed successfully',
        'pending': 'Pod is waiting to be scheduled',
        'containercreating': 'Container image is being pulled and container is starting',
        'podinitializing': 'Init containers are running',
        'imagepullbackoff': 'Failed to pull container image — retrying with exponential backoff',
        'errimagepull': 'Error pulling container image — check image name and registry access',
        'crashloopbackoff': 'Container keeps crashing — check logs for the root cause',
        'oomkilled': 'Container was killed due to exceeding memory limits',
        'error': 'Container exited with an error',
        'failed': 'Pod has failed',
        'terminating': 'Pod is being deleted',
        'containerstatusunknown': 'Container status cannot be determined — node may be unreachable',
      };
      return reasons[s.toLowerCase()] || s;
    };
    // Sort: app pods first, system pods second
    const sortedPods = [...pods].sort((a, b) => (a.isSystem ? 1 : 0) - (b.isSystem ? 1 : 0));
    $('#node-pods-body').innerHTML = sortedPods.length ? sortedPods.map(p => `<tr>
      <td>${p.name || ''}</td><td>${p.namespace || ''}</td>
      <td>${p.isSystem ? badge('System', 'gray') : badge('App', 'blue')}</td>
      <td>${fmtCPU(p.cpuRequest)}</td><td>${fmtMem(p.memRequest)}</td>
      <td title="${podStatusReason(p.status)}">${badge(p.status || 'Unknown', podStatusColor(p.status))}</td>
    </tr>`).join('') : '<tr><td colspan="6" style="color:var(--text-muted)">No pods found</td></tr>';
    makeSortable($('#node-pods-table'));

  } catch (e) {
    container().innerHTML = errorMsg(`Failed to load node ${name}: ${e.message}`);
  }
}
