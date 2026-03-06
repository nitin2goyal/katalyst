import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, utilBar, utilClass, badge, errorMsg, GiB, TiB } from '../utils.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, attachPagination, exportCSV, cardHeader, columnToggle, attachColumnToggle } from '../components.js';

// Format disk type + size as a concise string, e.g. "Hyperdisk Balanced 100G"
function fmtDisk(type, sizeGB) {
  if (!type && !sizeGB) return '';
  const name = (type || '').replace(/^pd-/, 'PD ').replace(/^hyperdisk-/, 'Hyperdisk ').replace(/\b\w/g, c => c.toUpperCase());
  return (name + (sizeGB ? ' ' + sizeGB + 'G' : '')).trim();
}

// All Nodes column definitions for column toggle
const nodeColumns = [
  { key: 'name', label: 'Name', default: true },
  { key: 'nodeGroup', label: 'Node Group', default: true },
  { key: 'instanceType', label: 'Instance Type', default: true },
  { key: 'disk', label: 'Disk', default: false },
  { key: 'diskUtil', label: 'Disk Util', default: true },
  { key: 'cpuUtil', label: 'CPU Util', default: true },
  { key: 'memUtil', label: 'Mem Util', default: true },
  { key: 'cpuAlloc', label: 'CPU Alloc', default: true },
  { key: 'memAlloc', label: 'Mem Alloc', default: true },
  { key: 'pods', label: 'Pods', default: true },
  { key: 'cordoned', label: 'Cordoned', default: true },
  { key: 'drainStatus', label: 'Drain Status', default: false },
  { key: 'costHr', label: 'Cost/hr', default: false },
];

export async function renderNodes(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const [ngs, nodes, empty] = await Promise.all([
      api('/nodegroups?pageSize=1000'), api('/nodes?pageSize=1000'), api('/nodegroups/empty').catch(() => null),
    ]);
    const ngList = toArray(ngs, 'nodeGroups');
    const nodeList = toArray(nodes, 'nodes');
    const emptyList = toArray(empty, 'nodeGroups');
    const emptyNames = new Set(emptyList.map(e => e.name || e.id));

    // Collect unique node groups for filters
    const nodeGroups = [...new Set(nodeList.map(n => n.nodeGroup).filter(Boolean))];

    // Compute cluster totals from node list
    const totalCPU = nodeList.reduce((s, n) => s + (n.cpuCapacity || 0), 0);
    const totalMem = nodeList.reduce((s, n) => s + (n.memCapacity || 0), 0);
    const fmtCPU = totalCPU >= 1000000 ? (totalCPU / 1000).toFixed(0) : (totalCPU / 1000).toFixed(1);
    const fmtMem = totalMem >= TiB ? (totalMem / TiB).toFixed(1) + ' TiB' : (totalMem / GiB).toFixed(1) + ' GiB';

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Nodes & Node Groups</h1><p>Cluster node infrastructure</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Node Groups</div><div class="value blue">${ngList.length}</div></div>
        <div class="kpi-card"><div class="label">Total Nodes</div><div class="value">${nodeList.length}</div></div>
        <div class="kpi-card"><div class="label">Total CPU</div><div class="value">${fmtCPU} cores</div></div>
        <div class="kpi-card"><div class="label">Total Memory</div><div class="value">${fmtMem}</div></div>
        <div class="kpi-card"><div class="label">Empty Groups</div><div class="value ${emptyList.length ? 'amber' : ''}">${emptyList.length}</div></div>
      </div>
      <div class="card" id="ng-card">
        ${cardHeader('Node Groups', `<label class="toggle-label" style="margin-right:12px;font-size:13px;cursor:pointer"><input type="checkbox" id="show-empty-ng"> Show empty groups</label><button class="btn btn-gray btn-sm" onclick="window.__exportNgCSV()">Export CSV</button>`)}
        <div class="table-wrap"><table id="ng-table">
          <thead><tr><th>Name</th><th>Instance Type</th><th>Disk</th><th>Count</th><th>Min</th><th>Max</th><th>GPUs</th><th>Total Cores</th><th>Total Memory</th><th>CPU Util</th><th>Mem Util</th><th>CPU Alloc</th><th>Mem Alloc</th><th>Cluster</th><th>Cost/mo</th></tr></thead>
          <tbody id="ng-body"></tbody>
        </table></div>
      </div>
      <div class="card" id="nodes-card">
        ${cardHeader('All Nodes', columnToggle(nodeColumns) + '<button class="btn btn-gray btn-sm" onclick="window.__exportNodesCSV()" style="margin-left:8px">Export CSV</button>')}
        ${filterBar({
          placeholder: 'Search nodes...',
          filters: [
            { key: '1', label: 'Node Group', options: nodeGroups },
          ]
        })}
        <div class="table-wrap"><table id="node-table">
          <thead><tr><th>Name</th><th>Node Group</th><th>Instance Type</th><th>Disk</th><th>Disk Util</th><th>CPU Util</th><th>Mem Util</th><th>CPU Alloc</th><th>Mem Alloc</th><th>Pods</th><th>Cordoned</th><th>Drain Status</th><th>Cost/hr</th></tr></thead>
          <tbody id="node-body"></tbody>
        </table></div>
      </div>`;

    // ── Node Groups table with empty group filtering ──
    function renderNgRows(showEmpty) {
      const filtered = showEmpty ? ngList : ngList.filter(ng => {
        return !(ng.isEmpty || emptyNames.has(ng.name) || emptyNames.has(ng.id));
      });
      $('#ng-body').innerHTML = filtered.length ? filtered.map(ng => {
        const isEmpty = ng.isEmpty || emptyNames.has(ng.name) || emptyNames.has(ng.id);
        return `<tr class="clickable-row ${isEmpty ? 'warning-row' : ''}" onclick="location.hash='#/nodegroups/${ng.id || ''}'">
          <td>${ng.name || ng.id || ''}${isEmpty ? ' ' + badge('EMPTY', 'amber') : ''}${ng.hasGPU ? ' ' + badge('GPU', 'purple') : ''}</td>
          <td>${ng.instanceType || ''}</td>
          <td style="font-size:0.8rem">${fmtDisk(ng.diskType, ng.diskSizeGB)}</td>
          <td>${ng.currentCount ?? 0}</td><td>${ng.minCount ?? ''}</td><td>${ng.maxCount ?? ''}</td>
          <td>${ng.totalGPUs ? ng.totalGPUs : '-'}</td>
          <td>${ng.totalCPU ? (ng.totalCPU / 1000).toFixed(0) : 0}</td>
          <td>${ng.totalMemory ? (ng.totalMemory / GiB).toFixed(1) + ' Gi' : '0 Gi'}</td>
          <td><strong class="${utilClass(ng.cpuUtilPct || 0)}">${fmtPct(ng.cpuUtilPct)}</strong></td>
          <td><strong class="${utilClass(ng.memUtilPct || 0)}">${fmtPct(ng.memUtilPct)}</strong></td>
          <td><strong class="${utilClass(ng.cpuAllocPct || 0)}">${fmtPct(ng.cpuAllocPct)}</strong></td>
          <td><strong class="${utilClass(ng.memAllocPct || 0)}">${fmtPct(ng.memAllocPct)}</strong></td>
          <td>${ng.sprCluster || ''}</td>
          <td>${fmt$(ng.monthlyCostUSD)}</td>
        </tr>`;
      }).join('') : '<tr><td colspan="15" style="color:var(--text-muted)">No node groups</td></tr>';
    }

    // Initial render without empty groups
    renderNgRows(false);

    // Empty groups checkbox toggle
    const emptyCheckbox = $('#show-empty-ng');
    if (emptyCheckbox) {
      emptyCheckbox.addEventListener('change', () => {
        renderNgRows(emptyCheckbox.checked);
        // Re-apply sort after re-render
        makeSortable($('#ng-table'));
      });
    }

    $('#node-body').innerHTML = nodeList.length ? nodeList.map(n => `<tr class="clickable-row" onclick="location.hash='#/nodes/${n.name || ''}'">
      <td>${n.name || ''}</td><td>${n.nodeGroup || ''}</td><td>${n.instanceType || ''}</td>
      <td style="font-size:0.8rem">${fmtDisk(n.diskType, n.diskSizeGB)}</td>
      <td><strong class="${utilClass(n.diskUtilPct || 0)}">${fmtPct(n.diskUtilPct)}</strong></td>
      <td><strong class="${utilClass(n.cpuUtilPct || 0)}">${fmtPct(n.cpuUtilPct)}</strong></td>
      <td><strong class="${utilClass(n.memUtilPct || 0)}">${fmtPct(n.memUtilPct)}</strong></td>
      <td><strong class="${utilClass(n.cpuAllocPct || 0)}">${fmtPct(n.cpuAllocPct)}</strong></td>
      <td><strong class="${utilClass(n.memAllocPct || 0)}">${fmtPct(n.memAllocPct)}</strong></td>
      <td>${n.appPodCount ?? n.podCount ?? ''}${n.systemPodCount ? ' <span style="color:var(--text-muted)">+ ' + n.systemPodCount + ' sys</span>' : ''}</td>
      <td>${n.unschedulable ? '<span class="badge badge-red">Yes</span>' : '<span style="color:var(--text-muted)">No</span>'}</td>
      <td>${n.drainStatus ? `<span class="badge ${n.drainStatus === 'Drained' ? 'badge-green' : 'badge-amber'}">${n.drainStatus}</span>` : ''}</td>
      <td>${fmt$(n.hourlyCostUSD)}</td>
    </tr>`).join('') : '<tr><td colspan="13" style="color:var(--text-muted)">No nodes</td></tr>';

    // Make NG table sortable and apply default sort on Total Cores (col index 7) descending
    makeSortable($('#ng-table'));
    const ngTable = $('#ng-table');
    const ngHeaders = ngTable.querySelectorAll('thead th');
    if (ngHeaders.length > 7 && !ngHeaders[7].classList.contains('sorted-asc') && !ngHeaders[7].classList.contains('sorted-desc')) {
      ngHeaders[7].click(); // ascending
      ngHeaders[7].click(); // descending (highest first)
    }

    makeSortable($('#node-table'));
    const pag = attachPagination($('#node-table'));

    // Attach column toggle for All Nodes table
    const nodesCard = document.getElementById('nodes-card');
    attachColumnToggle(nodesCard, $('#node-table'), 'kopt-node-columns', nodeColumns);

    // Attach filter handlers
    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#node-table'), pag);

    // CSV exports
    window.__exportNgCSV = () => {
      exportCSV(['Name', 'Instance Type', 'Disk Type', 'Disk Size (GB)', 'Count', 'Min', 'Max', 'GPUs', 'Total Cores', 'Total Memory (GiB)', 'CPU Util %', 'Mem Util %', 'CPU Alloc %', 'Mem Alloc %', 'Cluster', 'Cost/mo'],
        ngList.map(ng => [ng.name, ng.instanceType, ng.diskType || '', ng.diskSizeGB || '', ng.currentCount, ng.minCount, ng.maxCount, ng.totalGPUs || 0, ng.totalCPU ? (ng.totalCPU / 1000).toFixed(0) : 0, ng.totalMemory ? (ng.totalMemory / GiB).toFixed(1) : 0, (ng.cpuUtilPct||0).toFixed(1), (ng.memUtilPct||0).toFixed(1), (ng.cpuAllocPct||0).toFixed(1), (ng.memAllocPct||0).toFixed(1), ng.sprCluster || '', ng.monthlyCostUSD]),
        'katalyst-nodegroups.csv');
    };
    window.__exportNodesCSV = () => {
      exportCSV(['Name', 'Node Group', 'Instance Type', 'Disk Type', 'Disk Size (GB)', 'Disk Util %', 'CPU Util %', 'Mem Util %', 'CPU Alloc %', 'Mem Alloc %', 'App Pods', 'System Pods', 'Total Pods', 'Cost/hr'],
        nodeList.map(n => [n.name, n.nodeGroup, n.instanceType, n.diskType || '', n.diskSizeGB || '', (n.diskUtilPct||0).toFixed(1), (n.cpuUtilPct||0).toFixed(1), (n.memUtilPct||0).toFixed(1), (n.cpuAllocPct||0).toFixed(1), (n.memAllocPct||0).toFixed(1), n.appPodCount ?? '', n.systemPodCount ?? '', n.podCount, n.hourlyCostUSD]),
        'katalyst-nodes.csv');
    };

  } catch (e) {
    container().innerHTML = errorMsg('Failed to load node data: ' + e.message);
  }
}
