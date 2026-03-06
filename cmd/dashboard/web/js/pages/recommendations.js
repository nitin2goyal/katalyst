import { api, apiPost } from '../api.js';
import { $, toArray, fmt$, fmtPct, errorMsg, escapeHtml, esc } from '../utils.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, attachPagination, exportCSV, badge, cardHeader, toast } from '../components.js';
import { computeRecommendations } from '../recommendations-engine.js';

let _isComputed = false;

// Persist computed rec statuses in localStorage so they survive refresh
const REC_STATUS_KEY = 'kopt-rec-statuses';
function loadRecStatuses() { try { return JSON.parse(localStorage.getItem(REC_STATUS_KEY)) || {}; } catch { return {}; } }
function saveRecStatus(id, status) { const s = loadRecStatuses(); s[id] = status; localStorage.setItem(REC_STATUS_KEY, JSON.stringify(s)); }

export async function renderRecsTab(targetEl) {
  targetEl.innerHTML = skeleton(5);
  try {
    const [recs, summary, configData] = await Promise.all([
      api('/recommendations?pageSize=1000'),
      api('/recommendations/summary').catch(() => null),
      api('/config').catch(() => ({})),
    ]);
    const autoApproveRightsizer = (configData.autoApprove || {}).rightsizer ?? false;
    let recList = toArray(recs, 'recommendations');
    _isComputed = false;
    // Fallback: compute from live node data when API returns empty
    if (!recList.length) {
      const computed = await computeRecommendations();
      recList = computed.recommendations;
      _isComputed = true;
    }
    // Filter out spot recommendations (spot feature removed)
    recList = recList.filter(r => { const t = (r.type || r.Type || '').toLowerCase(); return t !== 'spot' && !t.includes('spot'); });
    // Restore persisted statuses for computed recommendations
    const savedStatuses = loadRecStatuses();
    recList.forEach(r => {
      const id = r.id || r.ID;
      if (id && savedStatuses[id]) {
        r.status = savedStatuses[id];
      }
    });
    // When auto-approve is enabled, mark matching recs as auto-approved
    recList.forEach(r => {
      const t = (r.type || r.Type || '').toLowerCase();
      const st = (r.status || r.Status || '').toLowerCase();
      if (st !== 'pending') return;
      if (autoApproveRightsizer && t === 'rightsizing') {
        r.status = 'approved';
        r._autoApproved = true;
      }
    });
    const pending = recList.filter(r => (r.status || r.Status) === 'pending').length;
    const approved = recList.filter(r => (r.status || r.Status) === 'approved').length;
    const totalSavings = recList.reduce((s, r) => s + (r.estimatedSavings || 0), 0);

    const recsByType = {};
    recList.forEach(r => { const t = r.type || r.Type || '?'; recsByType[t] = (recsByType[t] || 0) + 1; });
    const savingsByType = {};
    recList.forEach(r => { const t = r.type || r.Type || '?'; savingsByType[t] = (savingsByType[t] || 0) + (r.estimatedSavings || 0); });
    const debugSrc = _isComputed ? 'Client-side (JS engine)' : (recList[0]?.id?.startsWith('computed-') ? 'Backend (Go engine)' : 'API (CRDs)');

    const types = [...new Set(recList.map(r => r.type || r.Type || r.category).filter(Boolean))];
    const statuses = ['pending', 'approved', 'dismissed'];

    targetEl.innerHTML = `
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total</div><div class="value">${recList.length}</div></div>
        <div class="kpi-card"><div class="label">Pending</div><div class="value amber">${pending}</div></div>
        <div class="kpi-card"><div class="label">Approved</div><div class="value green">${approved}</div></div>
        <div class="kpi-card"><div class="label">Est. Total Savings</div><div class="value green">${fmt$(totalSavings)}</div><div class="sub">if all pending approved</div></div>
      </div>
      ${_isComputed ? '<div class="info-banner">These recommendations are computed from live cluster data. Switch to OPTIMIZE mode to enable automatic execution.</div>' : ''}
      ${autoApproveRightsizer ? '<div class="info-banner" style="border-color:var(--green)">Auto-approve is ON for: Rightsizing (once per workload).</div>' : ''}
      <details class="debug-panel" id="debug-panel" style="margin-bottom:16px">
        <summary style="cursor:pointer;font-size:12px;color:var(--text-muted);padding:8px 0">Data Validation (click to expand)</summary>
        <div class="card" id="debug-panel-content" style="font-size:12px;line-height:1.8;padding:16px;margin-top:8px">
          <div style="color:var(--text-muted)">Loading debug data...</div>
        </div>
      </details>
      <div class="card">
        ${cardHeader('All Recommendations', '<button class="btn btn-gray btn-sm" onclick="window.__exportRecsCSV()">Export CSV</button>')}
        ${filterBar({
          placeholder: 'Search recommendations...',
          filters: [
            { key: '0', label: 'Type', options: types },
            { key: '5', label: 'Status', options: statuses },
          ]
        })}
        <div class="table-wrap"><table id="rec-table">
          <thead><tr><th>Type</th><th>Target</th><th>Description</th><th>Savings</th><th>Confidence</th><th>Status</th><th>Actions</th></tr></thead>
          <tbody id="rec-body"></tbody>
        </table></div>
      </div>`;

    renderRecTable(recList);
    makeSortable($('#rec-table'));
    const pag = attachPagination($('#rec-table'));

    const fb = targetEl.querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#rec-table'), pag);

    // Handle button clicks (approve/dismiss) and row expand via event delegation
    targetEl.querySelector('#rec-body')?.addEventListener('click', (e) => {
      const btn = e.target.closest('button[data-action]');
      if (btn) {
        e.stopPropagation();
        const recId = btn.dataset.recId;
        if (btn.dataset.action === 'approve') window.__approveRec(recId);
        else if (btn.dataset.action === 'dismiss') window.__dismissRec(recId);
        return;
      }
      const row = e.target.closest('tr');
      if (!row || e.target.closest('button')) return;
      const descCell = row.querySelector('.rec-desc');
      if (descCell) descCell.classList.toggle('rec-desc-expanded');
    });

    window.__exportRecsCSV = () => {
      exportCSV(['Type', 'Target', 'Description', 'Savings', 'Confidence %', 'Status'],
        recList.map(r => [r.type || r.Type, r.target || r.resource, r.description || r.summary, r.estimatedSavings, r.confidence ?? '', r.status || r.Status]),
        'katalyst-recommendations.csv');
    };

    // Lazy-load debug data only when user expands the panel
    const debugPanel = document.getElementById('debug-panel');
    if (debugPanel) {
      debugPanel.addEventListener('toggle', async () => {
        if (!debugPanel.open || debugPanel.dataset.loaded) return;
        debugPanel.dataset.loaded = '1';
        try {
          const [clusterNodes, clusterWorkloads, debugData] = await Promise.all([
            api('/nodes?pageSize=1000').catch(() => []),
            api('/workloads?pageSize=1000').catch(() => []),
            api('/recommendations/debug').catch(() => null),
          ]);
          const nodeList = Array.isArray(clusterNodes) ? clusterNodes : (clusterNodes?.data || clusterNodes?.nodes || []);
          const wlList = Array.isArray(clusterWorkloads) ? clusterWorkloads : (clusterWorkloads?.data || clusterWorkloads?.workloads || []);
          const totalNodeCost = nodeList.reduce((s, n) => s + (n.hourlyCostUSD || 0) * 730.5, 0);
          const nodesWithUsage = nodeList.filter(n => (n.cpuUsed || 0) > 0 || (n.memUsed || 0) > 0).length;
          const avgNodeCPU = nodeList.length ? nodeList.reduce((s, n) => s + ((n.cpuUsed || 0) / (n.cpuCapacity || 1) * 100), 0) / nodeList.length : 0;
          const avgNodeMem = nodeList.length ? nodeList.reduce((s, n) => s + ((n.memUsed || 0) / (n.memCapacity || 1) * 100), 0) / nodeList.length : 0;
          const emptyNodes = nodeList.filter(n => (n.podCount || 0) === 0).length;
          const gpuNodes = nodeList.filter(n => n.isGPU).length;
          const content = document.getElementById('debug-panel-content');
          if (content) {
            content.innerHTML = `
              <div style="display:grid;grid-template-columns:1fr 1fr;gap:8px 24px">
                <div><strong>Data Source:</strong> ${debugSrc}</div>
                <div><strong>Summary API:</strong> ${summary ? escapeHtml(JSON.stringify(summary)) : 'null/empty'}</div>
                <div><strong>Nodes:</strong> ${nodeList.length} total, ${emptyNodes} empty, ${gpuNodes} GPU</div>
                <div><strong>Node Metrics:</strong> ${nodesWithUsage}/${nodeList.length} have usage data</div>
                <div><strong>Avg Node Util:</strong> CPU ${avgNodeCPU.toFixed(1)}%, Mem ${avgNodeMem.toFixed(1)}%</div>
                <div><strong>Total Cluster Cost:</strong> ${fmt$(totalNodeCost)}/mo (from node hourly costs)</div>
                <div><strong>Workloads:</strong> ${wlList.length}</div>
                <div><strong>Savings as % of cost:</strong> ${totalNodeCost > 0 ? (totalSavings / totalNodeCost * 100).toFixed(1) : 0}%</div>
              </div>
              <div style="margin-top:12px"><strong>Recs by type:</strong> ${Object.entries(recsByType).map(([t, c]) => `${t}: ${c}`).join(' | ')}</div>
              <div><strong>Savings by type:</strong> ${Object.entries(savingsByType).map(([t, s]) => `${t}: ${fmt$(s)}`).join(' | ')}</div>
              <div style="margin-top:8px;color:var(--text-muted)">Top 5 nodes by cost: ${nodeList.sort((a, b) => (b.hourlyCostUSD || 0) - (a.hourlyCostUSD || 0)).slice(0, 5).map(n => `${esc(n.name)}: $${((n.hourlyCostUSD || 0) * 730.5).toFixed(0)}/mo (CPU: ${((n.cpuUsed || 0) / (n.cpuCapacity || 1) * 100).toFixed(0)}%, Mem: ${((n.memUsed || 0) / (n.memCapacity || 1) * 100).toFixed(0)}%)`).join(' | ')}</div>
              ${debugData ? `<div style="margin-top:12px;border-top:1px solid var(--border);padding-top:12px">
                <strong>Backend Engine Debug (/api/v1/recommendations/debug):</strong>
                <pre style="font-size:11px;overflow-x:auto;margin-top:4px;color:var(--text-muted)">${escapeHtml(JSON.stringify(debugData, null, 2))}</pre>
              </div>` : ''}`;
          }
        } catch (err) {
          const content = document.getElementById('debug-panel-content');
          if (content) content.innerHTML = `<div style="color:var(--red)">Failed to load debug data: ${escapeHtml(err.message)}</div>`;
        }
      });
    }
  } catch (e) {
    targetEl.innerHTML = errorMsg('Failed to load recommendations: ' + escapeHtml(e.message));
  }
}

function confBadge(conf) {
  if (conf == null) return '-';
  const pct = conf < 1 ? Math.round(conf * 100) : Math.round(conf);
  const cls = pct >= 85 ? 'green' : pct >= 65 ? 'amber' : 'red';
  return badge(pct + '%', cls);
}

function renderRecTable(recList) {
  $('#rec-body').innerHTML = recList.length ? recList.map(r => {
    const st = r.status || r.Status || 'unknown';
    const isAutoApproved = r._autoApproved;
    const statusBadge = isAutoApproved ? badge('Auto-Approved', 'green')
      : st === 'pending' ? badge('Pending', 'amber')
      : st === 'approved' ? badge('Approved', 'green')
      : st === 'dismissed' ? badge('Dismissed', 'gray')
      : badge(escapeHtml(st), 'blue');
    const desc = r.description || r.summary || '';
    const actions = (st === 'pending' && !isAutoApproved) ? `
      <button class="btn btn-green btn-sm" data-action="approve" data-rec-id="${escapeHtml(r.id || r.ID || '')}">Approve</button>
      <button class="btn btn-gray btn-sm" data-action="dismiss" data-rec-id="${escapeHtml(r.id || r.ID || '')}" style="margin-left:4px">Dismiss</button>
    ` : '';
    return `<tr class="clickable-row" data-rec-id="${escapeHtml(r.id || r.ID || '')}">
      <td>${badge(escapeHtml(r.type || r.Type || r.category || ''), 'blue')}</td>
      <td><strong>${escapeHtml(r.target || r.resource || '')}</strong></td>
      <td class="rec-desc"><span class="rec-desc-text">${escapeHtml(desc)}</span><span class="rec-tooltip">${escapeHtml(desc)}</span></td>
      <td class="value green">${fmt$(r.estimatedSavings)}</td>
      <td>${confBadge(r.confidence)}</td>
      <td class="rec-status">${statusBadge}</td>
      <td class="rec-actions" style="white-space:nowrap">${actions}</td>
    </tr>`;
  }).join('') : '<tr><td colspan="7" style="color:var(--text-muted)">No recommendations</td></tr>';
}

function updateRecRow(id, newStatus) {
  const row = document.querySelector(`tr[data-rec-id="${CSS.escape(id)}"]`);
  if (!row) return;
  const statusCell = row.querySelector('.rec-status');
  const actionsCell = row.querySelector('.rec-actions');
  if (statusCell) {
    const badgeCls = newStatus === 'approved' ? 'green' : 'gray';
    const label = newStatus === 'approved' ? 'Approved' : 'Dismissed';
    statusCell.innerHTML = `<span class="badge ${badgeCls}">${label}</span>`;
  }
  if (actionsCell) actionsCell.innerHTML = '';
}

window.__approveRec = async function (id) {
  if (id && id.startsWith('computed-')) {
    updateRecRow(id, 'approved');
    saveRecStatus(id, 'approved');
    toast('Recommendation approved.', 'success');
    return;
  }
  try {
    await apiPost(`/recommendations/${id}/approve`);
    updateRecRow(id, 'approved');
    toast('Recommendation approved', 'success');
  } catch (e) { toast('Failed to approve: ' + e.message, 'error'); }
};

window.__dismissRec = async function (id) {
  if (id && id.startsWith('computed-')) {
    updateRecRow(id, 'dismissed');
    saveRecStatus(id, 'dismissed');
    toast('Recommendation dismissed.', 'info');
    return;
  }
  try {
    await apiPost(`/recommendations/${id}/dismiss`);
    updateRecRow(id, 'dismissed');
    toast('Recommendation dismissed', 'info');
  } catch (e) { toast('Failed to dismiss: ' + e.message, 'error'); }
};
