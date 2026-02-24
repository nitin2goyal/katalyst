import { api } from '../api.js';
import { $, toArray, timeAgo, errorMsg } from '../utils.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, badge, cardHeader, emptyState } from '../components.js';

const container = () => $('#page-container');

export async function renderNotifications() {
  container().innerHTML = skeleton(5);
  try {
    const data = await api('/notifications').catch(() => null);
    const nd = data || {};
    const alerts = toArray(nd, 'alerts');
    const channels = toArray(nd, 'channels');
    const active = alerts.filter(a => a.status === 'active' || a.status === 'firing');
    const critical = active.filter(a => a.severity === 'critical');
    const warnings = active.filter(a => a.severity === 'warning');
    const resolved = alerts.filter(a => a.status === 'resolved');

    container().innerHTML = `
      <div class="page-header"><h1>Notifications &amp; Alerts</h1><p>Kubernetes optimization alerts and notification channels</p></div>
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Active Alerts</div><div class="value red">${active.length}</div></div>
        <div class="kpi-card"><div class="label">Critical</div><div class="value red">${critical.length}</div></div>
        <div class="kpi-card"><div class="label">Warnings</div><div class="value amber">${warnings.length}</div></div>
        <div class="kpi-card"><div class="label">Resolved (24h)</div><div class="value green">${resolved.length}</div></div>
      </div>
      <div class="card">
        ${cardHeader('Active Alerts')}
        ${filterBar({ placeholder: 'Search alerts...', filters: [
          { key: '1', label: 'Severity', options: ['critical', 'warning', 'info'] },
          { key: '2', label: 'Category', options: [...new Set(alerts.map(a => a.category).filter(Boolean))] }
        ] })}
        <div class="table-wrap"><table id="alerts-table">
          <thead><tr><th>Time</th><th>Severity</th><th>Category</th><th>Message</th><th>Target</th><th>Status</th></tr></thead>
          <tbody id="alerts-body"></tbody>
        </table></div>
      </div>
      <div class="card">
        ${cardHeader('Notification Channels')}
        <div class="channels-grid" id="channels-grid">
          ${channels.length ? channels.map(ch => `
            <div class="channel-card">
              <div class="channel-header">
                <span class="channel-type">${badge(ch.type || 'webhook', 'blue')}</span>
                <span class="channel-status">${badge(ch.enabled ? 'active' : 'disabled', ch.enabled ? 'green' : 'gray')}</span>
              </div>
              <div class="channel-name">${ch.name || ch.type || ''}</div>
              <div class="channel-detail">${ch.target || ''}</div>
            </div>
          `).join('') : emptyState('&#128276;', 'No notification channels configured')}
        </div>
      </div>`;

    // Alerts table
    const severityClass = s => s === 'critical' ? 'red' : s === 'warning' ? 'amber' : 'blue';
    const statusClass = s => s === 'active' || s === 'firing' ? 'red' : s === 'resolved' ? 'green' : 'gray';
    $('#alerts-body').innerHTML = alerts.length ? alerts.map(a => `<tr>
      <td>${timeAgo(a.timestamp)}</td>
      <td>${badge(a.severity || 'info', severityClass(a.severity))}</td>
      <td>${a.category || ''}</td>
      <td>${a.message || ''}</td>
      <td><strong>${a.target || ''}</strong></td>
      <td>${badge(a.status || 'unknown', statusClass(a.status))}</td>
    </tr>`).join('') : '<tr><td colspan="6" style="color:var(--text-muted)">No alerts</td></tr>';
    makeSortable($('#alerts-table'));

    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#alerts-table'));
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load notifications: ' + e.message);
  }
}
