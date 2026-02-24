import { api } from '../api.js';
import { $, errorMsg } from '../utils.js';
import { skeleton, badge, cardHeader } from '../components.js';

const container = () => $('#page-container');

export async function renderPolicies() {
  container().innerHTML = skeleton(5);
  try {
    const data = await api('/policies');
    const templates = data?.nodeTemplates || [];
    const policies = data?.schedulingPolicies || [];

    container().innerHTML = `
      <div class="page-header"><h1>Node Templates &amp; Policies</h1><p>Manage placement constraints, instance preferences, and scheduling rules</p></div>
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Node Templates</div><div class="value blue">${templates.length}</div></div>
        <div class="kpi-card"><div class="label">Scheduling Policies</div><div class="value blue">${policies.length}</div></div>
        <div class="kpi-card"><div class="label">Active Policies</div><div class="value green">${policies.filter(p => p.enabled).length}</div></div>
        <div class="kpi-card"><div class="label">Instance Families</div><div class="value">${[...new Set(templates.flatMap(t => t.instanceFamilies || []))].length}</div></div>
      </div>

      <div class="card">
        ${cardHeader('Node Templates')}
        <div class="templates-grid" id="templates-grid"></div>
      </div>

      <div class="card">
        ${cardHeader('Scheduling Policies')}
        <div class="table-wrap"><table id="policies-table">
          <thead><tr><th>Policy</th><th>Type</th><th>Target</th><th>Description</th><th>Status</th></tr></thead>
          <tbody>${policies.length ? policies.map(p => `<tr>
            <td style="font-weight:600">${p.name}</td>
            <td>${badge(p.type, 'blue')}</td>
            <td><code style="font-size:12px;background:var(--bg);padding:2px 6px;border-radius:4px">${p.target}</code></td>
            <td style="white-space:normal;color:var(--text-muted);font-size:12px">${p.description}</td>
            <td>${p.enabled ? badge('Active', 'green') : badge('Inactive', 'gray')}</td>
          </tr>`).join('') : '<tr><td colspan="5" style="color:var(--text-muted)">No policies configured</td></tr>'}</tbody>
        </table></div>
      </div>`;

    // Render node template cards
    const tg = $('#templates-grid');
    tg.innerHTML = templates.map(t => {
      const families = (t.instanceFamilies || []).join(', ');
      const excluded = (t.excludedTypes || []);
      const taints = (t.taints || []);
      const labels = t.labels || {};
      const zones = (t.zones || []).join(', ');
      const labelEntries = Object.entries(labels);
      return `<div class="template-card">
        <div class="template-header">
          <span class="template-name">${t.name}</span>
          ${badge(t.capacityType || 'on-demand', t.capacityType === 'spot' ? 'amber' : 'blue')}
        </div>
        <div class="template-desc">${t.description || ''}</div>
        <div class="template-details">
          <div class="template-row"><span class="template-label">Instance Families</span><span>${families}</span></div>
          <div class="template-row"><span class="template-label">Architecture</span><span>${t.architecture || 'amd64'}</span></div>
          <div class="template-row"><span class="template-label">Node Range</span><span>${t.minNodes} - ${t.maxNodes}</span></div>
          <div class="template-row"><span class="template-label">Zones</span><span>${zones}</span></div>
          ${excluded.length ? `<div class="template-row"><span class="template-label">Excluded Types</span><span class="red">${excluded.join(', ')}</span></div>` : ''}
          ${taints.length ? `<div class="template-row"><span class="template-label">Taints</span><span>${taints.map(t => `<code style="font-size:11px;background:var(--bg);padding:1px 4px;border-radius:3px">${t.key}=${t.value}:${t.effect}</code>`).join(' ')}</span></div>` : ''}
          ${labelEntries.length ? `<div class="template-row"><span class="template-label">Labels</span><span>${labelEntries.map(([k, v]) => `<code style="font-size:11px;background:var(--bg);padding:1px 4px;border-radius:3px">${k}=${v}</code>`).join(' ')}</span></div>` : ''}
        </div>
      </div>`;
    }).join('');

  } catch (e) {
    container().innerHTML = errorMsg('Failed to load policies: ' + e.message);
  }
}
