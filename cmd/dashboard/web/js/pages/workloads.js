import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, fmtCPU, fmtMem, errorMsg, esc } from '../utils.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, attachPagination, exportCSV, cardHeader, badge, columnToggle, attachColumnToggle } from '../components.js';
import { addCleanup } from '../router.js';

const COLUMNS = [
  { key: 'namespace',       label: 'Namespace',        default: false },
  { key: 'kind',            label: 'Kind',             default: false },
  { key: 'name',            label: 'Name',             default: true },
  { key: 'replicas',        label: 'Replicas',         default: true },
  { key: 'cpuReq',          label: 'CPU Req',          default: true },
  { key: 'cpuLim',          label: 'CPU Lim',          default: true },
  { key: 'memReq',          label: 'Mem Req',          default: false },
  { key: 'memLim',          label: 'Mem Lim',          default: false },
  { key: 'totalCPU',        label: 'Total CPU',        default: true },
  { key: 'totalCPULim',     label: 'Total CPU Lim',    default: true },
  { key: 'totalMem',        label: 'Total Mem',        default: true },
  { key: 'pdbMinAvail',     label: 'PDB MinAvail',     default: true },
  { key: 'pdbMaxUnavail',   label: 'PDB MaxUnavail',   default: true },
  { key: 'pdbDisruptAllow', label: 'Disruptions OK',   default: true },
  { key: 'min',             label: 'Min',              default: true },
  { key: 'max',             label: 'Max',              default: true },
  { key: 'autoscaler',      label: 'Autoscaler',       default: false },
  { key: 'xmx',             label: 'Xmx',             default: false },
  { key: 'image',           label: 'Image',            default: false },
  { key: 'cpuEff',          label: 'CPU Eff.',         default: false },
  { key: 'memEff',          label: 'Mem Eff.',         default: false },
  { key: 'wasted',          label: 'Wasted',           default: false },
];

const COL_STORAGE_KEY = 'kopt-wl-columns';

// Compute column index by key for filter selects
function colIndex(key) {
  return COLUMNS.findIndex(c => c.key === key);
}

export async function renderWorkloads(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const wls = await api('/workloads?pageSize=1000');
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

    const headerActions = columnToggle(COLUMNS) +
      ' <button class="btn btn-gray btn-sm" onclick="window.__exportWlCSV()">Export CSV</button>';

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Workloads</h1><p>Workload resource usage, efficiency, and scaling status</p></div>' : ''}
      ${metricsAvail === false ? '<div class="info-banner">Metrics Server unavailable — efficiency data is estimated from resource requests. Install metrics-server for accurate usage data.</div>' : ''}
      ${metricsAvail && podsWithMetrics < totalPods ? `<div class="info-banner">Metrics available for ${podsWithMetrics}/${totalPods} pods. Pods without metrics show 0% efficiency.</div>` : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total Workloads</div><div class="value blue">${wlList.length}</div></div>
        <div class="kpi-card"><div class="label">Avg CPU Efficiency</div><div class="value ${avgCpuEff > 100 ? 'red' : avgCpuEff >= 70 ? 'green' : avgCpuEff >= 40 ? 'amber' : 'red'}">${fmtPct(avgCpuEff)}</div>${avgCpuEff > 100 ? '<div class="sub">exceeds requests</div>' : ''}</div>
        <div class="kpi-card"><div class="label">Avg Mem Efficiency</div><div class="value ${avgMemEff > 100 ? 'red' : avgMemEff >= 70 ? 'green' : avgMemEff >= 40 ? 'amber' : 'red'}">${fmtPct(avgMemEff)}</div>${avgMemEff > 100 ? '<div class="sub">exceeds requests — OOM risk</div>' : ''}</div>
        <div class="kpi-card"><div class="label">Wasted Cost</div><div class="value red">${fmt$(totalWasted)}</div><div class="sub">resources requested but unused</div></div>
      </div>
      <div class="card">
        ${cardHeader('Workload List', headerActions)}
        ${filterBar({
          placeholder: 'Search workloads...',
          filters: [
            { key: String(colIndex('namespace')), label: 'Namespace', options: namespaces },
            { key: String(colIndex('kind')), label: 'Kind', options: kinds },
          ]
        })}
        <div class="table-wrap"><table id="wl-table">
          <thead><tr>${COLUMNS.map(c => `<th>${c.label}</th>`).join('')}</tr></thead>
          <tbody id="wl-body"></tbody>
        </table></div>
      </div>`;

    const effBadge = (pct, hasMetrics) => {
      if (pct == null) return '<span style="color:var(--text-muted)">-</span>';
      if (hasMetrics === false) return '<span style="color:var(--text-muted)" title="No metrics data">N/A</span>';
      const cls = pct > 100 ? 'red' : pct >= 70 ? 'green' : pct >= 40 ? 'amber' : 'red';
      return badge(fmtPct(pct), cls);
    };

    const shortImg = (img) => {
      if (!img) return '';
      const parts = img.split('/');
      const last = parts[parts.length - 1];
      return last.length > 40 ? last.substring(0, 37) + '...' : last;
    };

    const colSpan = COLUMNS.length;
    $('#wl-body').innerHTML = wlList.length ? wlList.map(w => {
      const eff = effMap[`${w.namespace}/${w.kind}/${w.name}`];
      const pdbMinAvail = w.pdbMinAvailable != null ? w.pdbMinAvailable : '-';
      const pdbMaxUnavail = w.pdbMaxUnavailable != null ? w.pdbMaxUnavailable : '-';
      const pdbDisruptAllow = w.pdbDisruptionsAllowed != null ? w.pdbDisruptionsAllowed : '-';
      return `<tr class="clickable-row" onclick="location.hash='#/workloads/${encodeURIComponent(w.namespace)}/${encodeURIComponent(w.kind)}/${encodeURIComponent(w.name)}'">
        <td>${esc(w.namespace || '')}</td>
        <td>${esc(w.kind || '')}</td>
        <td>${esc(w.name || '')}</td>
        <td>${w.replicas ?? ''}</td>
        <td>${fmtCPU(w.cpuRequest)}</td>
        <td>${fmtCPU(w.cpuLimit)}</td>
        <td>${fmtMem(w.memRequest)}</td>
        <td>${fmtMem(w.memLimit)}</td>
        <td>${fmtCPU(w.totalCPU)}</td>
        <td>${fmtCPU(w.totalCPULim)}</td>
        <td>${fmtMem(w.totalMem)}</td>
        <td>${w.pdbName ? badge(pdbMinAvail, 'blue') : '<span style="color:var(--text-muted)">-</span>'}</td>
        <td>${w.pdbName ? badge(pdbMaxUnavail, pdbMaxUnavail === '0' ? 'red' : 'amber') : '<span style="color:var(--text-muted)">-</span>'}</td>
        <td>${w.pdbName ? badge(String(pdbDisruptAllow), pdbDisruptAllow === 0 ? 'red' : 'green') : '<span style="color:var(--text-muted)">-</span>'}</td>
        <td>${w.minReplicas ?? '-'}</td>
        <td>${w.maxReplicas ?? '-'}</td>
        <td>${w.autoscaler ? badge(w.autoscaler, 'blue') : '-'}</td>
        <td>${esc(w.xmx || '-')}</td>
        <td title="${esc(w.image || '')}" style="max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11px;color:var(--text-muted)">${esc(shortImg(w.image))}</td>
        <td>${effBadge(eff?.cpuEfficiencyPct, eff?.hasMetrics)}</td>
        <td>${effBadge(eff?.memEfficiencyPct, eff?.hasMetrics)}</td>
        <td>${eff ? '<span class="red">' + fmt$(eff.wastedCostUSD) + '</span>' : '-'}</td>
      </tr>`;
    }).join('') : `<tr><td colspan="${colSpan}" style="color:var(--text-muted)">No workloads</td></tr>`;

    const tableEl = $('#wl-table');
    const cardEl = tableEl.closest('.card');

    // Apply column visibility
    attachColumnToggle(cardEl, tableEl, COL_STORAGE_KEY, COLUMNS);

    makeSortable(tableEl);
    // Default sort by Total Mem (descending) if no user sort state exists
    const wlHeaders = tableEl.querySelectorAll('thead th');
    const totalMemIdx = colIndex('totalMem');
    if (totalMemIdx >= 0 && wlHeaders[totalMemIdx] && !wlHeaders[totalMemIdx].classList.contains('sorted-asc') && !wlHeaders[totalMemIdx].classList.contains('sorted-desc')) {
      wlHeaders[totalMemIdx].click(); // ascending
      wlHeaders[totalMemIdx].click(); // descending (highest first)
    }
    const pag = attachPagination(tableEl);

    // Attach filter
    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, tableEl, pag);

    // CSV export (includes all columns regardless of visibility)
    window.__exportWlCSV = () => {
      exportCSV(
        ['Namespace', 'Kind', 'Name', 'Replicas', 'CPU Req', 'CPU Lim', 'Mem Req', 'Mem Lim',
         'Total CPU', 'Total CPU Lim', 'Total Mem',
         'PDB MinAvail', 'PDB MaxUnavail', 'Disruptions OK',
         'Min Replicas', 'Max Replicas', 'Autoscaler', 'Xmx',
         'Image', 'CPU Eff %', 'Mem Eff %', 'Wasted Cost'],
        wlList.map(w => {
          const eff = effMap[`${w.namespace}/${w.kind}/${w.name}`];
          return [w.namespace, w.kind, w.name, w.replicas,
            fmtCPU(w.cpuRequest), fmtCPU(w.cpuLimit), fmtMem(w.memRequest), fmtMem(w.memLimit),
            fmtCPU(w.totalCPU), fmtCPU(w.totalCPULim), fmtMem(w.totalMem),
            w.pdbMinAvailable ?? '', w.pdbMaxUnavailable ?? '', w.pdbDisruptionsAllowed ?? '',
            w.minReplicas ?? '', w.maxReplicas ?? '', w.autoscaler || '', w.xmx || '',
            w.image || '',
            eff?.cpuEfficiencyPct != null ? fmtPct(eff.cpuEfficiencyPct) : '',
            eff?.memEfficiencyPct != null ? fmtPct(eff.memEfficiencyPct) : '',
            eff?.wastedCostUSD != null ? fmt$(eff.wastedCostUSD) : ''];
        }),
        'katalyst-workloads.csv');
    };
    addCleanup(() => { delete window.__exportWlCSV; });
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load workloads: ' + e.message);
  }
}
