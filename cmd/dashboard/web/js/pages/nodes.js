import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, utilBar, utilClass, badge, errorMsg } from '../utils.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, exportCSV, cardHeader } from '../components.js';

export async function renderNodes(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const [ngs, nodes, empty] = await Promise.all([
      api('/nodegroups'), api('/nodes'), api('/nodegroups/empty').catch(() => null),
    ]);
    const ngList = toArray(ngs, 'nodeGroups');
    const nodeList = toArray(nodes, 'nodes');
    const emptyList = toArray(empty, 'nodeGroups');
    const emptyNames = new Set(emptyList.map(e => e.name || e.id));

    // Collect unique node groups and spot/on-demand for filters
    const nodeGroups = [...new Set(nodeList.map(n => n.nodeGroup).filter(Boolean))];
    const spotTypes = ['Spot', 'On-Demand'];

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Nodes & Node Groups</h1><p>Cluster node infrastructure</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Node Groups</div><div class="value blue">${ngList.length}</div></div>
        <div class="kpi-card"><div class="label">Total Nodes</div><div class="value">${nodeList.length}</div></div>
        <div class="kpi-card"><div class="label">Empty Groups</div><div class="value ${emptyList.length ? 'amber' : ''}">${emptyList.length}</div></div>
      </div>
      <div class="card">
        ${cardHeader('Node Groups', '<button class="btn btn-gray btn-sm" onclick="window.__exportNgCSV()">Export CSV</button>')}
        <div class="table-wrap"><table id="ng-table">
          <thead><tr><th>Name</th><th>Instance Type</th><th>Family</th><th>Count</th><th>Min</th><th>Max</th><th>CPU Util</th><th>Mem Util</th><th>Cost/mo</th></tr></thead>
          <tbody id="ng-body"></tbody>
        </table></div>
      </div>
      <div class="card">
        ${cardHeader('All Nodes', '<button class="btn btn-gray btn-sm" onclick="window.__exportNodesCSV()">Export CSV</button>')}
        ${filterBar({
          placeholder: 'Search nodes...',
          filters: [
            { key: '1', label: 'Node Group', options: nodeGroups },
            { key: '6', label: 'Type', options: spotTypes },
          ]
        })}
        <div class="table-wrap"><table id="node-table">
          <thead><tr><th>Name</th><th>Node Group</th><th>Instance Type</th><th>CPU Util</th><th>Mem Util</th><th>Pods</th><th>Spot</th><th>Cost/hr</th></tr></thead>
          <tbody id="node-body"></tbody>
        </table></div>
      </div>`;

    $('#ng-body').innerHTML = ngList.length ? ngList.map(ng => {
      const isEmpty = ng.isEmpty || emptyNames.has(ng.name) || emptyNames.has(ng.id);
      return `<tr class="clickable-row ${isEmpty ? 'warning-row' : ''}" onclick="location.hash='#/nodegroups/${ng.id || ''}'">
        <td>${ng.name || ng.id || ''}${isEmpty ? ' ' + badge('EMPTY', 'amber') : ''}</td>
        <td>${ng.instanceType || ''}</td><td>${ng.instanceFamily || ''}</td>
        <td>${ng.currentCount ?? 0}</td><td>${ng.minCount ?? ''}</td><td>${ng.maxCount ?? ''}</td>
        <td><strong class="${utilClass(ng.cpuUtilPct || 0)}">${fmtPct(ng.cpuUtilPct)}</strong></td>
        <td><strong class="${utilClass(ng.memUtilPct || 0)}">${fmtPct(ng.memUtilPct)}</strong></td>
        <td>${fmt$(ng.monthlyCostUSD)}</td>
      </tr>`;
    }).join('') : '<tr><td colspan="9" style="color:var(--text-muted)">No node groups</td></tr>';

    $('#node-body').innerHTML = nodeList.length ? nodeList.map(n => `<tr class="clickable-row" onclick="location.hash='#/nodes/${n.name || ''}'">
      <td>${n.name || ''}</td><td>${n.nodeGroup || ''}</td><td>${n.instanceType || ''}</td>
      <td><strong class="${utilClass(n.cpuUtilPct || 0)}">${fmtPct(n.cpuUtilPct)}</strong></td>
      <td><strong class="${utilClass(n.memUtilPct || 0)}">${fmtPct(n.memUtilPct)}</strong></td>
      <td>${n.podCount ?? ''}</td>
      <td>${n.isSpot ? badge('Spot', 'blue') : badge('On-Demand', 'gray')}</td>
      <td>${fmt$(n.hourlyCostUSD)}</td>
    </tr>`).join('') : '<tr><td colspan="8" style="color:var(--text-muted)">No nodes</td></tr>';

    makeSortable($('#ng-table'));
    makeSortable($('#node-table'));

    // Attach filter handlers
    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#node-table'));

    // CSV exports
    window.__exportNgCSV = () => {
      exportCSV(['Name', 'Instance Type', 'Family', 'Count', 'Min', 'Max', 'CPU Util %', 'Mem Util %', 'Cost/mo'],
        ngList.map(ng => [ng.name, ng.instanceType, ng.instanceFamily, ng.currentCount, ng.minCount, ng.maxCount, (ng.cpuUtilPct||0).toFixed(1), (ng.memUtilPct||0).toFixed(1), ng.monthlyCostUSD]),
        'koptimizer-nodegroups.csv');
    };
    window.__exportNodesCSV = () => {
      exportCSV(['Name', 'Node Group', 'Instance Type', 'CPU Util %', 'Mem Util %', 'Pods', 'Spot', 'Cost/hr'],
        nodeList.map(n => [n.name, n.nodeGroup, n.instanceType, (n.cpuUtilPct||0).toFixed(1), (n.memUtilPct||0).toFixed(1), n.podCount, n.isSpot ? 'Yes' : 'No', n.hourlyCostUSD]),
        'koptimizer-nodes.csv');
    };

  } catch (e) {
    container().innerHTML = errorMsg('Failed to load node data: ' + e.message);
  }
}
