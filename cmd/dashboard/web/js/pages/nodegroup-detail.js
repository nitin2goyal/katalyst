import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, utilBar, badge, errorMsg } from '../utils.js';
import { skeleton, breadcrumbs, makeSortable } from '../components.js';

const container = () => $('#page-container');

export async function renderNodeGroupDetail(params) {
  const id = params.id;
  container().innerHTML = skeleton(5);
  try {
    const [group, nodesData] = await Promise.all([
      api(`/nodegroups/${encodeURIComponent(id)}`),
      api(`/nodegroups/${encodeURIComponent(id)}/nodes`).catch(() => []),
    ]);
    const ng = group.nodeGroup || group;
    const nodeList = toArray(nodesData, 'nodes');

    container().innerHTML = `
      ${breadcrumbs([
        { label: 'Nodes', href: '#/nodes' },
        { label: ng.name || id }
      ])}
      <div class="page-header"><h1>${ng.name || id}</h1><p>Node group detail view</p></div>
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Instance Type</div><div class="value">${ng.instanceType || ''}</div></div>
        <div class="kpi-card"><div class="label">Family</div><div class="value">${ng.instanceFamily || ''}</div></div>
        <div class="kpi-card"><div class="label">Node Count</div><div class="value blue">${ng.currentCount ?? nodeList.length}</div><div class="sub">min: ${ng.minCount ?? '?'} / max: ${ng.maxCount ?? '?'}</div></div>
        <div class="kpi-card"><div class="label">CPU Utilization</div><div class="value">${fmtPct(ng.cpuUtilPct)}</div></div>
        <div class="kpi-card"><div class="label">Memory Utilization</div><div class="value">${fmtPct(ng.memUtilPct)}</div></div>
        <div class="kpi-card"><div class="label">Monthly Cost</div><div class="value">${fmt$(ng.monthlyCostUSD)}</div></div>
      </div>
      <div class="card">
        <h2>Scaling Configuration</h2>
        <div class="scaling-config">
          <div class="sc-item"><span class="sc-label">Desired</span><span class="sc-val">${ng.desiredCount ?? ng.currentCount ?? '?'}</span></div>
          <div class="sc-item"><span class="sc-label">Min</span><span class="sc-val">${ng.minCount ?? '?'}</span></div>
          <div class="sc-item"><span class="sc-label">Max</span><span class="sc-val">${ng.maxCount ?? '?'}</span></div>
          <div class="sc-item"><span class="sc-label">Total Pods</span><span class="sc-val">${ng.totalPods ?? '?'}</span></div>
        </div>
      </div>
      <div class="card">
        <h2>Nodes in this Group</h2>
        <div class="table-wrap"><table id="ng-nodes-table">
          <thead><tr><th>Name</th><th>Instance Type</th><th>CPU Util</th><th>Mem Util</th><th>Pods</th><th>Spot</th><th>Cost/hr</th></tr></thead>
          <tbody id="ng-nodes-body"></tbody>
        </table></div>
      </div>`;

    $('#ng-nodes-body').innerHTML = nodeList.length ? nodeList.map(n => `<tr class="clickable-row" onclick="location.hash='#/nodes/${n.name || ''}'">
      <td>${n.name || ''}</td><td>${n.instanceType || ''}</td>
      <td>${utilBar(n.cpuUtilPct)}</td><td>${utilBar(n.memUtilPct)}</td>
      <td>${n.podCount ?? ''}</td>
      <td>${n.isSpot ? badge('Spot', 'blue') : badge('On-Demand', 'gray')}</td>
      <td>${fmt$(n.hourlyCostUSD)}</td>
    </tr>`).join('') : '<tr><td colspan="7" style="color:var(--text-muted)">No nodes in this group</td></tr>';

    makeSortable($('#ng-nodes-table'));
  } catch (e) {
    container().innerHTML = errorMsg(`Failed to load node group ${id}: ${e.message}`);
  }
}
