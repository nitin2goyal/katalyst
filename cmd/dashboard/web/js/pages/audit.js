import { api } from '../api.js';
import { $, toArray, errorMsg, timeAgo, escapeHtml } from '../utils.js';
import { skeleton, filterBar, attachFilterHandlers, exportCSV, cardHeader, badge } from '../components.js';

const actionColors = {
  'recommendation.approved': 'green',
  'recommendation.dismissed': 'gray',
  'mode.changed': 'blue',
  'node.scaled': 'purple',
  'workload.rightsized': 'green',
  'config.updated': 'blue',
};

export async function renderAudit(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const data = await api('/audit');
    const events = toArray(data, 'events', 'audit');
    const actionTypes = [...new Set(events.map(e => e.action).filter(Boolean))];

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Audit Log</h1><p>Recent actions and changes in your cluster</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total Events</div><div class="value">${events.length}</div></div>
      </div>
      <div class="card">
        ${cardHeader('Recent Actions', '<button class="btn btn-gray btn-sm" onclick="window.__exportAuditCSV()">Export CSV</button>')}
        ${filterBar({
          placeholder: 'Search audit events...',
          filters: [
            { key: 'action', label: 'Action', options: actionTypes },
          ]
        })}
        <div class="audit-timeline" id="audit-timeline"></div>
      </div>`;

    const timeline = $('#audit-timeline');
    timeline.innerHTML = events.length ? events.map(ev => {
      const colorCls = actionColors[ev.action] || 'blue';
      return `<div class="audit-event">
        <div class="audit-event-dot ${colorCls}"></div>
        <div class="audit-event-content">
          <div class="audit-event-header">
            ${badge(ev.action || 'unknown', colorCls)}
            <span class="audit-event-time">${ev.timestamp ? timeAgo(ev.timestamp) : ''}</span>
          </div>
          <div class="audit-event-details">
            <span class="audit-event-target">${escapeHtml(ev.target || '')}</span>
            ${ev.details ? `<span class="audit-event-desc">${escapeHtml(ev.details)}</span>` : ''}
          </div>
          <div class="audit-event-meta">
            <span>by ${ev.user || 'system'}</span>
            ${ev.timestamp ? `<span>${new Date(ev.timestamp).toLocaleString()}</span>` : ''}
          </div>
        </div>
      </div>`;
    }).join('') : '<div class="empty-state"><div class="empty-state-msg">No audit events recorded</div></div>';

    // Filter handler (filters timeline items)
    const fb = container().querySelector('.filter-bar');
    if (fb) {
      const input = fb.querySelector('.filter-search');
      const selects = fb.querySelectorAll('.filter-select');
      function applyFilters() {
        const searchText = (input?.value || '').toLowerCase();
        const filterValues = {};
        selects.forEach(s => { if (s.value) filterValues[s.dataset.filter] = s.value.toLowerCase(); });
        timeline.querySelectorAll('.audit-event').forEach(el => {
          const text = el.textContent.toLowerCase();
          const matchSearch = !searchText || text.includes(searchText);
          let matchFilter = true;
          for (const [_, val] of Object.entries(filterValues)) {
            matchFilter = matchFilter && text.includes(val);
          }
          el.style.display = matchSearch && matchFilter ? '' : 'none';
        });
      }
      input?.addEventListener('input', applyFilters);
      selects.forEach(s => s.addEventListener('change', applyFilters));
    }

    // CSV export
    window.__exportAuditCSV = () => {
      exportCSV(['Timestamp', 'Action', 'Target', 'User', 'Details'],
        events.map(e => [e.timestamp, e.action, e.target, e.user, e.details]),
        'koptimizer-audit.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load audit log: ' + e.message);
  }
}
