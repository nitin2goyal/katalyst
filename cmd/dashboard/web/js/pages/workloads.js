import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, errorMsg } from '../utils.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, attachPagination, exportCSV, cardHeader, badge } from '../components.js';

function fmtCPUm(v) {
  if (v == null) return '0m';
  if (typeof v === 'number') return v + 'm';
  return v;
}

function fmtMemB(v) {
  if (v == null) return '0Mi';
  if (typeof v === 'number') {
    if (v >= 1073741824) return (v / 1073741824).toFixed(1) + 'Gi';
    if (v >= 1048576) return Math.round(v / 1048576) + 'Mi';
    if (v >= 1024) return Math.round(v / 1024) + 'Ki';
    return v + 'B';
  }
  return v;
}

export async function renderWorkloads(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const wls = await api('/workloads');
    const wlList = toArray(wls, 'workloads');

    const namespaces = [...new Set(wlList.map(w => w.namespace).filter(Boolean))];
    const kinds = [...new Set(wlList.map(w => w.kind).filter(Boolean))];

    // Fetch efficiency data
    const effData = await api('/workloads/efficiency').catch(() => null);
    const effWls = effData?.workloads || [];
    const effMap = {};
    effWls.forEach(e => { effMap[`${e.namespace}/${e.kind}/${e.name}`] = e; });
    const avgCpuEff = effData?.summary?.avgCPUEfficiency || 0;
    const avgMemEff = effData?.summary?.avgMemEfficiency || 0;
    const totalWasted = effData?.summary?.totalWastedCostUSD || 0;
    const metricsAvail = effData?.summary?.metricsAvailable;
    const podsWithMetrics = effData?.summary?.podsWithMetrics || 0;
    const totalPods = effData?.summary?.totalPods || 0;

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Workloads</h1><p>Workload resource usage, efficiency, and scaling status</p></div>' : ''}
      ${metricsAvail === false ? '<div class="info-banner">Metrics Server unavailable — efficiency data is estimated from resource requests. Install metrics-server for accurate usage data.</div>' : ''}
      ${metricsAvail && podsWithMetrics < totalPods ? `<div class="info-banner">Metrics available for ${podsWithMetrics}/${totalPods} pods. Pods without metrics show 0% efficiency.</div>` : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total Workloads</div><div class="value blue">${wlList.length}</div></div>
        <div class="kpi-card"><div class="label">Avg CPU Efficiency</div><div class="value ${avgCpuEff >= 70 ? 'green' : avgCpuEff >= 40 ? 'amber' : 'red'}">${fmtPct(avgCpuEff)}</div></div>
        <div class="kpi-card"><div class="label">Avg Mem Efficiency</div><div class="value ${avgMemEff >= 70 ? 'green' : avgMemEff >= 40 ? 'amber' : 'red'}">${fmtPct(avgMemEff)}</div></div>
        <div class="kpi-card"><div class="label">Wasted Cost</div><div class="value red">${fmt$(totalWasted)}</div><div class="sub">resources requested but unused</div></div>
      </div>
      <div class="card">
        ${cardHeader('Workload List', '<button class="btn btn-gray btn-sm" onclick="window.__exportWlCSV()">Export CSV</button>')}
        ${filterBar({
          placeholder: 'Search workloads...',
          filters: [
            { key: '0', label: 'Namespace', options: namespaces },
            { key: '1', label: 'Kind', options: kinds },
          ]
        })}
        <div class="table-wrap"><table id="wl-table">
          <thead><tr><th>Namespace</th><th>Kind</th><th>Name</th><th>Replicas</th><th>CPU Req</th><th>CPU Lim</th><th>Mem Req</th><th>Mem Lim</th><th>Total CPU</th><th>Total Mem</th><th>Image</th><th>CPU Eff.</th><th>Mem Eff.</th><th>Wasted</th></tr></thead>
          <tbody id="wl-body"></tbody>
        </table></div>
      </div>`;

    const effBadge = (pct, hasMetrics) => {
      if (pct == null) return '<span style="color:var(--text-muted)">-</span>';
      if (hasMetrics === false) return '<span style="color:var(--text-muted)" title="No metrics data">N/A</span>';
      const cls = pct >= 70 ? 'green' : pct >= 40 ? 'amber' : 'red';
      return badge(fmtPct(pct), cls);
    };

    const shortImg = (img) => {
      if (!img) return '';
      // Show last path segment + short digest/tag
      const parts = img.split('/');
      const last = parts[parts.length - 1];
      return last.length > 40 ? last.substring(0, 37) + '...' : last;
    };

    $('#wl-body').innerHTML = wlList.length ? wlList.map(w => {
      const eff = effMap[`${w.namespace}/${w.kind}/${w.name}`];
      return `<tr class="clickable-row" onclick="location.hash='#/workloads/${w.namespace}/${w.kind}/${w.name}'">
        <td>${w.namespace || ''}</td><td>${w.kind || ''}</td><td>${w.name || ''}</td>
        <td>${w.replicas ?? ''}</td>
        <td>${fmtCPUm(w.cpuRequest)}</td>
        <td>${fmtCPUm(w.cpuLimit)}</td>
        <td>${fmtMemB(w.memRequest)}</td>
        <td>${fmtMemB(w.memLimit)}</td>
        <td>${fmtCPUm(w.totalCPU)}</td>
        <td>${fmtMemB(w.totalMem)}</td>
        <td title="${w.image || ''}" style="max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11px;color:var(--text-muted)">${shortImg(w.image)}</td>
        <td>${effBadge(eff?.cpuEfficiencyPct, eff?.hasMetrics)}</td>
        <td>${effBadge(eff?.memEfficiencyPct, eff?.hasMetrics)}</td>
        <td>${eff ? '<span class="red">' + fmt$(eff.wastedCostUSD) + '</span>' : '-'}</td>
      </tr>`;
    }).join('') : '<tr><td colspan="14" style="color:var(--text-muted)">No workloads</td></tr>';

    makeSortable($('#wl-table'));
    const pag = attachPagination($('#wl-table'));

    // Attach filter
    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#wl-table'), pag);

    // CSV export
    window.__exportWlCSV = () => {
      exportCSV(['Namespace', 'Kind', 'Name', 'Replicas', 'CPU Req', 'CPU Lim', 'Mem Req', 'Mem Lim', 'Total CPU', 'Total Mem', 'Image', 'CPU Eff %', 'Mem Eff %', 'Wasted Cost'],
        wlList.map(w => {
          const eff = effMap[`${w.namespace}/${w.kind}/${w.name}`];
          return [w.namespace, w.kind, w.name, w.replicas, w.cpuRequest, w.cpuLimit, w.memRequest, w.memLimit, w.totalCPU, w.totalMem, w.image || '',
            eff?.cpuEfficiencyPct ?? '', eff?.memEfficiencyPct ?? '', eff?.wastedCostUSD ?? ''];
        }),
        'koptimizer-workloads.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load workloads: ' + e.message);
  }
}
