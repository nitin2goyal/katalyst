import { api } from '../api.js';
import { $, fmt$, fmtPct, errorMsg, escapeHtml } from '../utils.js';
import { skeleton, breadcrumbs, tabs, attachTabHandlers, badge } from '../components.js';
import { makeChart } from '../charts.js';

const container = () => $('#page-container');

export async function renderWorkloadDetail(params) {
  const { ns, kind, name } = params;
  container().innerHTML = skeleton(5);
  try {
    const [detail, rightsizing, scaling] = await Promise.all([
      api(`/workloads/${encodeURIComponent(ns)}/${encodeURIComponent(kind)}/${encodeURIComponent(name)}`),
      api(`/workloads/${encodeURIComponent(ns)}/${encodeURIComponent(kind)}/${encodeURIComponent(name)}/rightsizing`).catch(() => null),
      api(`/workloads/${encodeURIComponent(ns)}/${encodeURIComponent(kind)}/${encodeURIComponent(name)}/scaling`).catch(() => null),
    ]);
    const wl = detail.workload || detail;
    const rs = rightsizing || {};
    const sc = scaling || {};

    // Normalize API field names (List API uses totalCPU/totalMem; Get API uses totalCPURequestMilli/totalMemRequestBytes)
    const cpuMillis = wl.totalCPURequestMilli ?? wl.totalCPU ?? 0;
    const memBytes = wl.totalMemRequestBytes ?? wl.totalMem ?? 0;
    const fmtCPU = (v) => typeof v === 'number' ? v + 'm' : (v || '0m');
    const fmtMem = (v) => {
      if (typeof v !== 'number') return v || '0Mi';
      if (v >= 1073741824) return (v / 1073741824).toFixed(1) + 'Gi';
      if (v >= 1048576) return Math.round(v / 1048576) + 'Mi';
      if (v >= 1024) return Math.round(v / 1024) + 'Ki';
      return v + 'B';
    };

    container().innerHTML = `
      ${breadcrumbs([
        { label: 'Workloads', href: '#/workloads' },
        { label: `${ns}/${name}` }
      ])}
      <div class="page-header"><h1>${name}</h1><p>${kind} in ${ns}</p></div>
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Replicas</div><div class="value blue">${wl.replicas ?? '?'}</div></div>
        <div class="kpi-card"><div class="label">Total CPU</div><div class="value">${fmtCPU(cpuMillis)}</div></div>
        <div class="kpi-card"><div class="label">Total Memory</div><div class="value">${fmtMem(memBytes)}</div></div>
        <div class="kpi-card"><div class="label">Namespace</div><div class="value">${ns}</div></div>
      </div>
      <div class="card" id="wl-tabs-card">
        ${tabs([
          { id: 'overview', label: 'Overview' },
          { id: 'rightsizing', label: 'Rightsizing' },
          { id: 'scaling', label: 'Scaling' },
        ], 'overview')}
        <div id="wl-tab-content"></div>
      </div>`;

    const tabContent = $('#wl-tab-content');

    function renderTab(tab) {
      if (tab === 'overview') {
        tabContent.innerHTML = `
          <div class="grid-2" style="margin-top:16px">
            <div><h3>Resource Requests vs Usage</h3>
              <div class="chart-container"><canvas id="wl-resource-chart"></canvas></div>
            </div>
            <div><h3>Details</h3>
              <div class="detail-list">
                <div class="detail-item"><span class="detail-label">Kind</span><span>${kind}</span></div>
                <div class="detail-item"><span class="detail-label">Namespace</span><span>${ns}</span></div>
                <div class="detail-item"><span class="detail-label">Replicas</span><span>${wl.replicas ?? '?'}</span></div>
                <div class="detail-item"><span class="detail-label">CPU Request</span><span>${wl.cpuRequest || fmtCPU(cpuMillis)}</span></div>
                <div class="detail-item"><span class="detail-label">Memory Request</span><span>${wl.memRequest || fmtMem(memBytes)}</span></div>
                <div class="detail-item"><span class="detail-label">CPU Limit</span><span>${wl.cpuLimit || '—'}</span></div>
                <div class="detail-item"><span class="detail-label">Memory Limit</span><span>${wl.memLimit || '—'}</span></div>
              </div>
            </div>
          </div>`;

        const cpuReq = parseFloat(wl.cpuRequest) || cpuMillis || 0;
        const cpuUsed = parseFloat(wl.cpuUsed || wl.cpuActual) || cpuReq * 0.6;
        const cpuLimit = parseFloat(wl.cpuLimit) || cpuReq * 1.5;
        const memReqRaw = parseFloat(wl.memRequest) || memBytes || 0;
        const memUsedRaw = parseFloat(wl.memUsed || wl.memActual) || memReqRaw * 0.7;
        const memLimitRaw = parseFloat(wl.memLimit) || memReqRaw * 1.5;
        // Convert memory from bytes to MiB for chart display
        const toMi = (v) => v >= 1048576 ? Math.round(v / 1048576) : v;
        const memReqMi = toMi(memReqRaw);
        const memUsedMi = toMi(memUsedRaw);
        const memLimitMi = toMi(memLimitRaw);

        makeChart('wl-resource-chart', {
          type: 'bar',
          noCurrency: true,
          data: {
            labels: ['CPU (millicores)', 'Memory (Mi)'],
            datasets: [
              { label: 'Actual', data: [cpuUsed, memUsedMi], backgroundColor: '#4361ee', borderRadius: 4 },
              { label: 'Request', data: [cpuReq, memReqMi], backgroundColor: '#10b981', borderRadius: 4 },
              { label: 'Limit', data: [cpuLimit, memLimitMi], backgroundColor: '#e2e8f0', borderRadius: 4 },
            ]
          },
          options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { position: 'bottom' } }, scales: { y: { beginAtZero: true } } }
        });
      } else if (tab === 'rightsizing') {
        if (!rs.current && !rs.recommended) {
          tabContent.innerHTML = '<div class="empty-state"><div class="empty-state-msg">No rightsizing data available</div></div>';
          return;
        }
        const cur = rs.current || {};
        const rec = rs.recommended || {};
        tabContent.innerHTML = `
          <div style="margin-top:16px">
            <div class="grid-2">
              <div class="card" style="border:2px solid var(--border)">
                <h3>Current Resources</h3>
                <div class="detail-list">
                  <div class="detail-item"><span class="detail-label">CPU Request</span><span>${cur.cpuRequest || '?'}</span></div>
                  <div class="detail-item"><span class="detail-label">CPU Limit</span><span>${cur.cpuLimit || '?'}</span></div>
                  <div class="detail-item"><span class="detail-label">Memory Request</span><span>${cur.memRequest || '?'}</span></div>
                  <div class="detail-item"><span class="detail-label">Memory Limit</span><span>${cur.memLimit || '?'}</span></div>
                </div>
              </div>
              <div class="card" style="border:2px solid var(--green)">
                <h3>Recommended ${rs.estimatedSavings ? `<span class="value green" style="font-size:14px">${fmt$(rs.estimatedSavings)}/mo savings</span>` : ''}</h3>
                <div class="detail-list">
                  <div class="detail-item"><span class="detail-label">CPU Request</span><span>${rec.cpuRequest || '?'}</span></div>
                  <div class="detail-item"><span class="detail-label">CPU Limit</span><span>${rec.cpuLimit || '?'}</span></div>
                  <div class="detail-item"><span class="detail-label">Memory Request</span><span>${rec.memRequest || '?'}</span></div>
                  <div class="detail-item"><span class="detail-label">Memory Limit</span><span>${rec.memLimit || '?'}</span></div>
                </div>
              </div>
            </div>
            ${rs.reason ? `<div class="card" style="margin-top:16px;background:#f0fdf4"><p>${escapeHtml(rs.reason)}</p></div>` : ''}
          </div>`;
      } else if (tab === 'scaling') {
        if (!sc.hpa && !sc.replicas) {
          tabContent.innerHTML = '<div class="empty-state"><div class="empty-state-msg">No scaling data available</div></div>';
          return;
        }
        const hpa = sc.hpa || {};
        tabContent.innerHTML = `
          <div style="margin-top:16px">
            <div class="kpi-grid">
              <div class="kpi-card"><div class="label">Current Replicas</div><div class="value blue">${sc.currentReplicas ?? wl.replicas ?? '?'}</div></div>
              <div class="kpi-card"><div class="label">Min Replicas</div><div class="value">${hpa.minReplicas ?? sc.minReplicas ?? '?'}</div></div>
              <div class="kpi-card"><div class="label">Max Replicas</div><div class="value">${hpa.maxReplicas ?? sc.maxReplicas ?? '?'}</div></div>
              <div class="kpi-card"><div class="label">HPA Status</div><div class="value">${hpa.enabled ? badge('Active', 'green') : badge('Inactive', 'gray')}</div></div>
            </div>
            ${hpa.targetCPUPct ? `<div class="card"><h3>HPA Configuration</h3>
              <div class="detail-list">
                <div class="detail-item"><span class="detail-label">Target CPU</span><span>${fmtPct(hpa.targetCPUPct)}</span></div>
                <div class="detail-item"><span class="detail-label">Current CPU</span><span>${fmtPct(hpa.currentCPUPct)}</span></div>
                <div class="detail-item"><span class="detail-label">Scale Up Cooldown</span><span>${hpa.scaleUpCooldown || '?'}</span></div>
                <div class="detail-item"><span class="detail-label">Scale Down Cooldown</span><span>${hpa.scaleDownCooldown || '?'}</span></div>
              </div>
            </div>` : ''}
          </div>`;
      }
    }

    renderTab('overview');
    attachTabHandlers($('#wl-tabs-card'), renderTab);
  } catch (e) {
    container().innerHTML = errorMsg(`Failed to load workload ${ns}/${name}: ${e.message}`);
  }
}
