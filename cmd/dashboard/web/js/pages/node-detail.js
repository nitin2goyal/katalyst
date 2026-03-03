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
          <thead><tr><th>Name</th><th>Namespace</th><th>Type</th><th>CPU Req</th><th>CPU Used</th><th>CPU %</th><th>Mem Req</th><th>Mem Used</th><th>Mem %</th><th>Disk</th><th>Status</th></tr></thead>
          <tbody id="node-pods-body"></tbody>
        </table></div>
      </div>`;

    // Resource charts — 3 segments: Utilized / Allocated (unused) / Free
    const cpuUsedMilli = parseFloat(node.cpuUsed) || 0;
    const cpuReqMilli = parseFloat(node.cpuRequested) || 0;
    const cpuCapMilli = parseFloat(node.cpuCapacity) || 1;
    const cpuUtilCores = cpuUsedMilli / 1000;
    const cpuAllocCores = Math.max(0, cpuReqMilli - cpuUsedMilli) / 1000;
    const cpuFreeCores = Math.max(0, cpuCapMilli - cpuReqMilli) / 1000;
    makeBarChart('node-cpu-chart', {
      categories: ['CPU (cores)'],
      series: [
        { name: 'Utilized', data: [parseFloat(cpuUtilCores.toFixed(1))] },
        { name: 'Allocated (unused)', data: [parseFloat(cpuAllocCores.toFixed(1))] },
        { name: 'Free', data: [parseFloat(cpuFreeCores.toFixed(1))] },
      ],
      colors: ['#4361ee', '#93a8f4', '#e2e8f0'],
      horizontal: true,
      stacked: true,
      noCurrency: true,
    });

    const memUsedBytes = parseFloat(node.memUsed) || 0;
    const memReqBytes = parseFloat(node.memRequested) || 0;
    const memCapBytes = parseFloat(node.memCapacity) || 1;
    const memUtilGB = memUsedBytes / 1073741824;
    const memAllocGB = Math.max(0, memReqBytes - memUsedBytes) / 1073741824;
    const memFreeGB = Math.max(0, memCapBytes - memReqBytes) / 1073741824;
    makeBarChart('node-mem-chart', {
      categories: ['Memory (GB)'],
      series: [
        { name: 'Utilized', data: [parseFloat(memUtilGB.toFixed(1))] },
        { name: 'Allocated (unused)', data: [parseFloat(memAllocGB.toFixed(1))] },
        { name: 'Free', data: [parseFloat(memFreeGB.toFixed(1))] },
      ],
      colors: ['#8b5cf6', '#bba4f4', '#e2e8f0'],
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
    // Parse CPU/memory request to millicores/bytes for % calculation
    const parseCPUm = (v) => {
      if (typeof v === 'number') return v;
      const s = String(v || '').trim();
      if (s.endsWith('m')) return parseFloat(s);
      const n = parseFloat(s);
      return isNaN(n) ? 0 : n * 1000; // assume cores if no unit
    };
    const parseMemB = (v) => {
      if (typeof v === 'number') return v;
      const s = String(v || '').trim();
      const m = s.match(/^([\d.]+)\s*(Ti|Gi|Mi|Ki|B)?$/i);
      if (!m) return 0;
      const n = parseFloat(m[1]);
      const unit = (m[2] || '').toLowerCase();
      if (unit === 'ti') return n * 1099511627776;
      if (unit === 'gi') return n * 1073741824;
      if (unit === 'mi') return n * 1048576;
      if (unit === 'ki') return n * 1024;
      return n;
    };
    // Format millicores for pod usage display
    const fmtCPUm = (v) => {
      const m = typeof v === 'number' ? v : parseCPUm(v);
      return m >= 1000 ? (m / 1000).toFixed(1) + ' cores' : m + 'm';
    };
    const fmtMemB = (v) => {
      const b = typeof v === 'number' ? v : parseMemB(v);
      if (b >= 1073741824) return (b / 1073741824).toFixed(1) + ' Gi';
      return Math.round(b / 1048576) + ' Mi';
    };

    // Sort: app pods first, system pods second
    const sortedPods = [...pods].sort((a, b) => (a.isSystem ? 1 : 0) - (b.isSystem ? 1 : 0));
    $('#node-pods-body').innerHTML = sortedPods.length ? sortedPods.map(p => {
      const cpuReqM = parseCPUm(p.cpuRequest);
      const memReqB = parseMemB(p.memRequest);
      const cpuUsedM = p.cpuUsed != null ? parseCPUm(p.cpuUsed) : null;
      const memUsedB = p.memUsed != null ? parseMemB(p.memUsed) : null;
      // Utilization % relative to pod's own request
      const cpuPct = cpuUsedM != null && cpuReqM > 0 ? (cpuUsedM / cpuReqM * 100) : null;
      const memPctVal = memUsedB != null && memReqB > 0 ? (memUsedB / memReqB * 100) : null;
      return `<tr>
      <td>${p.name || ''}</td><td>${p.namespace || ''}</td>
      <td>${p.isSystem ? badge('System', 'gray') : badge('App', 'blue')}</td>
      <td>${fmtCPU(p.cpuRequest)}</td>
      <td>${cpuUsedM != null ? fmtCPUm(cpuUsedM) : '<span style="color:var(--text-muted)">-</span>'}</td>
      <td>${cpuPct != null ? utilBar(cpuPct) : '<span style="color:var(--text-muted)">-</span>'}</td>
      <td>${fmtMem(p.memRequest)}</td>
      <td>${memUsedB != null ? fmtMemB(memUsedB) : '<span style="color:var(--text-muted)">-</span>'}</td>
      <td>${memPctVal != null ? utilBar(memPctVal) : '<span style="color:var(--text-muted)">-</span>'}</td>
      <td>${p.diskUsage ? fmtMem(p.diskUsage) : '-'}</td>
      <td title="${podStatusReason(p.status)}">${badge(p.status || 'Unknown', podStatusColor(p.status))}</td>
    </tr>`;
    }).join('') : '<tr><td colspan="11" style="color:var(--text-muted)">No pods found</td></tr>';
    makeSortable($('#node-pods-table'));

  } catch (e) {
    container().innerHTML = errorMsg(`Failed to load node ${name}: ${e.message}`);
  }
}
