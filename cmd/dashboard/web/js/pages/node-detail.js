import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, fmtCPU, fmtMem, utilBar, badge, errorMsg, esc, GiB, parseCPUm, parseMemB, fmtCPUm, fmtMemB, podStatusColor, chartColors } from '../utils.js';
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
      <div class="page-header"><h1>${esc(name)}</h1><p>Node detail view</p></div>
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Instance Type</div><div class="value">${esc(node.instanceType || '')}</div></div>
        <div class="kpi-card"><div class="label">Node Group</div><div class="value"><a href="#/nodegroups/${encodeURIComponent(node.nodeGroupId || node.nodeGroup || '')}" class="link">${esc(node.nodeGroup || '')}</a></div></div>
        <div class="kpi-card"><div class="label">CPU Utilization</div><div class="value">${fmtPct(node.cpuUtilPct)}</div></div>
        <div class="kpi-card"><div class="label">Memory Utilization</div><div class="value">${fmtPct(node.memUtilPct)}</div></div>
        <div class="kpi-card"><div class="label">Pod Count</div><div class="value blue">${node.appPodCount != null ? node.appPodCount + ' <span class="text-xs-muted" style="font-size:0.6em">+ ' + (node.systemPodCount || 0) + ' sys</span>' : (node.podCount ?? pods.length)}</div></div>
        <div class="kpi-card"><div class="label">Boot Disk</div><div class="value" style="font-size:0.9rem">${node.diskType ? (node.diskType.replace(/^pd-/, 'PD ').replace(/^hyperdisk-/, 'Hyperdisk ').replace(/\b\w/g, c => c.toUpperCase())) + (node.diskSizeGB ? ' ' + node.diskSizeGB + 'G' : '') : '-'}</div></div>
        <div class="kpi-card"><div class="label">Hourly Cost</div><div class="value">${fmt$(node.hourlyCostUSD)}</div></div>
      </div>
      ${disks.length ? `<div class="card">
        <h2>Attached disks <span class="text-small-muted" style="font-weight:400">${disks.length} disk${disks.length > 1 ? 's' : ''} &middot; ${totalDiskGiB} GiB total</span></h2>
        <div class="table-wrap"><table>
          <thead><tr><th>Device</th><th>Type</th><th>Size</th><th>IOPS</th><th>Throughput</th><th>Encrypted</th></tr></thead>
          <tbody>${disks.map(d => `<tr>
            <td><code class="code-inline">${esc(d.name || '')}</code></td>
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
          <thead><tr><th>Name</th><th>Namespace</th><th>Type</th><th>Ready</th><th>CPU Req</th><th>CPU Used</th><th>CPU %</th><th>Mem Req</th><th>Mem Used</th><th>Mem %</th><th>Disk</th><th>Status</th></tr></thead>
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
      colors: [chartColors.primary, chartColors.primaryLight, chartColors.muted],
      horizontal: true,
      stacked: true,
      noCurrency: true,
    });

    const memUsedBytes = parseFloat(node.memUsed) || 0;
    const memReqBytes = parseFloat(node.memRequested) || 0;
    const memCapBytes = parseFloat(node.memCapacity) || 1;
    const memUtilGB = memUsedBytes / GiB;
    const memAllocGB = Math.max(0, memReqBytes - memUsedBytes) / GiB;
    const memFreeGB = Math.max(0, memCapBytes - memReqBytes) / GiB;
    makeBarChart('node-mem-chart', {
      categories: ['Memory (GB)'],
      series: [
        { name: 'Utilized', data: [parseFloat(memUtilGB.toFixed(1))] },
        { name: 'Allocated (unused)', data: [parseFloat(memAllocGB.toFixed(1))] },
        { name: 'Free', data: [parseFloat(memFreeGB.toFixed(1))] },
      ],
      colors: [chartColors.purple, chartColors.purpleLight, chartColors.muted],
      horizontal: true,
      stacked: true,
      noCurrency: true,
    });

    // Pod status tooltip reasons (unique to node-detail)
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
    $('#node-pods-body').innerHTML = sortedPods.length ? sortedPods.map(p => {
      const cpuReqM = parseCPUm(p.cpuRequest);
      const memReqB = parseMemB(p.memRequest);
      const cpuUsedM = p.cpuUsed != null ? parseCPUm(p.cpuUsed) : null;
      const memUsedB = p.memUsed != null ? parseMemB(p.memUsed) : null;
      // Utilization % relative to pod's own request
      const cpuPct = cpuUsedM != null && cpuReqM > 0 ? (cpuUsedM / cpuReqM * 100) : null;
      const memPctVal = memUsedB != null && memReqB > 0 ? (memUsedB / memReqB * 100) : null;
      const readyColor = p.ready && p.ready.split('/')[0] === p.ready.split('/')[1] ? 'green' : 'amber';
      return `<tr>
      <td>${esc(p.name || '')}</td><td>${esc(p.namespace || '')}</td>
      <td>${p.isSystem ? badge('System', 'gray') : badge('App', 'blue')}</td>
      <td>${p.ready ? `<span style="color:var(--${readyColor});font-weight:500">${p.ready}</span>` : '-'}</td>
      <td>${fmtCPUm(cpuReqM)}</td>
      <td>${cpuUsedM != null ? fmtCPUm(cpuUsedM) : '<span style="color:var(--text-muted)">-</span>'}</td>
      <td>${cpuPct != null ? utilBar(cpuPct) : '<span style="color:var(--text-muted)">-</span>'}</td>
      <td>${fmtMemB(memReqB)}</td>
      <td>${memUsedB != null ? fmtMemB(memUsedB) : '<span style="color:var(--text-muted)">-</span>'}</td>
      <td>${memPctVal != null ? utilBar(memPctVal) : '<span style="color:var(--text-muted)">-</span>'}</td>
      <td>${p.diskUsage ? fmtMem(p.diskUsage) : '-'}</td>
      <td title="${esc(podStatusReason(p.status))}">${badge(p.status || 'Unknown', podStatusColor(p.status))}</td>
    </tr>`;
    }).join('') : '<tr><td colspan="12" style="color:var(--text-muted)">No pods found</td></tr>';
    makeSortable($('#node-pods-table'));

  } catch (e) {
    container().innerHTML = errorMsg(`Failed to load node ${name}: ${e.message}`);
  }
}
