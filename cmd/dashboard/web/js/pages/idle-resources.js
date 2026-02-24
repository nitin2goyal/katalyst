import { api } from '../api.js';
import { $, fmt$, fmtPct, errorMsg } from '../utils.js';
import { skeleton, makeSortable, exportCSV, cardHeader, badge } from '../components.js';

export async function renderIdleResources(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const data = await api('/idle-resources');
    const summary = data?.summary || {};
    const nodes = data?.nodes || [];
    const workloads = data?.workloads || [];
    const pvcs = data?.orphanedPVCs || [];

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Idle Resources</h1><p>Detect underutilized nodes, workloads, and orphaned storage</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Idle Nodes</div><div class="value red">${summary.totalIdleNodes || 0}</div><div class="sub">CPU &lt; 15% for 1h+</div></div>
        <div class="kpi-card"><div class="label">Idle Workloads</div><div class="value amber">${summary.totalIdleWorkloads || 0}</div><div class="sub">utilization below threshold</div></div>
        <div class="kpi-card"><div class="label">Wasted Cost</div><div class="value red">${fmt$(summary.totalWastedCostUSD)}</div><div class="sub">monthly from idle resources</div></div>
        <div class="kpi-card"><div class="label">Avg Idle Duration</div><div class="value">${(summary.avgIdleDurationHrs || 0).toFixed(0)}h</div><div class="sub">average idle time</div></div>
      </div>

      <div class="card">
        ${cardHeader('Idle Nodes', '<button class="btn btn-gray btn-sm" onclick="window.__exportIdleNodesCSV()">Export CSV</button>')}
        <div class="table-wrap"><table id="idle-nodes-table">
          <thead><tr><th>Node</th><th>Instance Type</th><th>CPU Util</th><th>Mem Util</th><th>Idle Since</th><th>Wasted Cost</th><th>Reason</th></tr></thead>
          <tbody>${nodes.length ? nodes.map(n => `<tr class="warning-row">
            <td><a href="#/nodes/${n.name}" class="link">${n.name}</a></td>
            <td>${n.instanceType || ''}</td>
            <td>${badge(fmtPct(n.cpuUtilPct), n.cpuUtilPct < 10 ? 'red' : 'amber')}</td>
            <td>${badge(fmtPct(n.memUtilPct), n.memUtilPct < 15 ? 'red' : 'amber')}</td>
            <td>${n.idleSinceHrs}h</td>
            <td class="red">${fmt$(n.wastedCostUSD)}/mo</td>
            <td style="white-space:normal;max-width:300px;font-size:12px;color:var(--text-muted)">${n.reason || ''}</td>
          </tr>`).join('') : '<tr><td colspan="7" style="color:var(--text-muted)">No idle nodes detected</td></tr>'}</tbody>
        </table></div>
      </div>

      <div class="card">
        ${cardHeader('Idle Workloads', '<button class="btn btn-gray btn-sm" onclick="window.__exportIdleWlCSV()">Export CSV</button>')}
        <div class="table-wrap"><table id="idle-wl-table">
          <thead><tr><th>Namespace</th><th>Kind</th><th>Name</th><th>CPU Used</th><th>Mem Used</th><th>Replicas</th><th>Idle Since</th><th>Wasted</th><th>Reason</th></tr></thead>
          <tbody>${workloads.length ? workloads.map(w => `<tr class="warning-row">
            <td>${w.namespace}</td>
            <td>${w.kind}</td>
            <td><a href="#/workloads/${w.namespace}/${w.kind}/${w.name}" class="link">${w.name}</a></td>
            <td>${badge(fmtPct(w.cpuUsedPct), w.cpuUsedPct < 10 ? 'red' : 'amber')}</td>
            <td>${badge(fmtPct(w.memUsedPct), w.memUsedPct < 15 ? 'red' : 'amber')}</td>
            <td>${w.replicas}</td>
            <td>${w.idleSinceHrs}h</td>
            <td class="red">${fmt$(w.wastedCostUSD)}/mo</td>
            <td style="white-space:normal;max-width:300px;font-size:12px;color:var(--text-muted)">${w.reason || ''}</td>
          </tr>`).join('') : '<tr><td colspan="9" style="color:var(--text-muted)">No idle workloads detected</td></tr>'}</tbody>
        </table></div>
      </div>

      <div class="card">
        ${cardHeader('Orphaned PVCs', '<button class="btn btn-gray btn-sm" onclick="window.__exportPvcCSV()">Export CSV</button>')}
        <p style="color:var(--text-muted);font-size:13px;margin-bottom:12px">Persistent Volume Claims not mounted by any pod</p>
        <div class="table-wrap"><table id="pvc-table">
          <thead><tr><th>Name</th><th>Namespace</th><th>Size</th><th>Age</th><th>Monthly Cost</th></tr></thead>
          <tbody>${pvcs.length ? pvcs.map(p => `<tr class="warning-row">
            <td>${p.name}</td>
            <td>${p.namespace}</td>
            <td>${p.sizeGB} GB</td>
            <td>${Math.floor(p.ageHours / 24)}d</td>
            <td class="red">${fmt$(p.monthlyCostUSD)}</td>
          </tr>`).join('') : '<tr><td colspan="5" style="color:var(--text-muted)">No orphaned PVCs</td></tr>'}</tbody>
        </table></div>
      </div>`;

    makeSortable($('#idle-nodes-table'));
    makeSortable($('#idle-wl-table'));
    makeSortable($('#pvc-table'));

    // CSV exports
    window.__exportIdleNodesCSV = () => {
      exportCSV(['Node', 'Instance Type', 'CPU Util %', 'Mem Util %', 'Idle Hours', 'Wasted Cost', 'Reason'],
        nodes.map(n => [n.name, n.instanceType, n.cpuUtilPct, n.memUtilPct, n.idleSinceHrs, n.wastedCostUSD, n.reason]),
        'koptimizer-idle-nodes.csv');
    };
    window.__exportIdleWlCSV = () => {
      exportCSV(['Namespace', 'Kind', 'Name', 'CPU Used %', 'Mem Used %', 'Replicas', 'Idle Hours', 'Wasted Cost', 'Reason'],
        workloads.map(w => [w.namespace, w.kind, w.name, w.cpuUsedPct, w.memUsedPct, w.replicas, w.idleSinceHrs, w.wastedCostUSD, w.reason]),
        'koptimizer-idle-workloads.csv');
    };
    window.__exportPvcCSV = () => {
      exportCSV(['Name', 'Namespace', 'Size GB', 'Age Days', 'Monthly Cost'],
        pvcs.map(p => [p.name, p.namespace, p.sizeGB, Math.floor(p.ageHours / 24), p.monthlyCostUSD]),
        'koptimizer-orphaned-pvcs.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load idle resources: ' + e.message);
  }
}
