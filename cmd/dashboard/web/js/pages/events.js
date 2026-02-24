import { api } from '../api.js';
import { $, toArray, timeAgo, errorMsg, escapeHtml } from '../utils.js';
import { skeleton, tabs, attachTabHandlers, filterBar, attachFilterHandlers, badge, cardHeader } from '../components.js';

const eventColor = (action) => {
  if (!action) return 'gray';
  if (action.includes('approved') || action.includes('scaled') || action.includes('rightsized')) return 'green';
  if (action.includes('dismissed') || action.includes('error')) return 'red';
  if (action.includes('warning') || action.includes('alert')) return 'amber';
  if (action.includes('config') || action.includes('mode')) return 'purple';
  return 'blue';
};

const eventCategory = (action) => {
  if (!action) return 'other';
  if (action.includes('recommendation') || action.includes('rightsiz') || action.includes('savings')) return 'optimization';
  if (action.includes('scale') || action.includes('node')) return 'scaling';
  if (action.includes('config') || action.includes('mode')) return 'configuration';
  if (action.includes('alert') || action.includes('security')) return 'security';
  return 'other';
};

export async function renderEvents(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const data = await api('/events').catch(() => api('/audit').catch(() => []));
    const events = toArray(data, 'events');
    const today = new Date().toDateString();
    const todayEvents = events.filter(e => new Date(e.timestamp).toDateString() === today);
    const systemEvents = events.filter(e => e.user === 'system');
    const userEvents = events.filter(e => e.user !== 'system');

    const catTabs = [
      { id: 'all', label: 'All (' + events.length + ')' },
      { id: 'optimization', label: 'Optimization' },
      { id: 'scaling', label: 'Scaling' },
      { id: 'configuration', label: 'Config' },
      { id: 'security', label: 'Security' },
    ];

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Event Log</h1><p>Activity stream and system events</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total Events</div><div class="value blue">${events.length}</div></div>
        <div class="kpi-card"><div class="label">Today</div><div class="value">${todayEvents.length}</div></div>
        <div class="kpi-card"><div class="label">System Events</div><div class="value">${systemEvents.length}</div></div>
        <div class="kpi-card"><div class="label">User Events</div><div class="value purple">${userEvents.length}</div></div>
      </div>
      <div class="card">
        ${cardHeader('Activity Stream')}
        ${tabs(catTabs, 'all')}
        ${filterBar({ placeholder: 'Search events...' })}
        <div class="audit-timeline" id="events-timeline"></div>
      </div>`;

    function renderTimeline(category) {
      const filtered = category === 'all' ? events : events.filter(e => eventCategory(e.action) === category);
      const timeline = $('#events-timeline');
      timeline.innerHTML = filtered.length ? filtered.map(e => {
        const color = eventColor(e.action);
        const jsonStr = JSON.stringify(e, null, 2);
        return `<div class="audit-event" data-category="${eventCategory(e.action)}">
          <div class="audit-event-dot ${color}"></div>
          <div class="audit-event-content">
            <div class="audit-event-header">
              ${badge(e.action || 'event', color)} <span class="audit-event-time">${timeAgo(e.timestamp)}</span>
            </div>
            <div class="audit-event-details">
              <span class="audit-event-target">${e.target || ''}</span>
              <span class="audit-event-desc">${e.details || ''}</span>
            </div>
            <div class="audit-event-meta">
              <span>User: ${e.user || 'system'}</span>
              <span>${e.timestamp ? new Date(e.timestamp).toLocaleString() : ''}</span>
              <button class="btn btn-gray btn-sm event-json-toggle" onclick="this.nextElementSibling.style.display=this.nextElementSibling.style.display==='none'?'block':'none'">JSON</button>
              <pre class="event-json" style="display:none">${escapeHtml(jsonStr)}</pre>
            </div>
          </div>
        </div>`;
      }).join('') : '<div style="color:var(--text-muted);padding:24px;text-align:center">No events in this category</div>';
    }

    renderTimeline('all');

    // Tab handling
    const tabsEl = container().querySelector('.tabs');
    if (tabsEl) attachTabHandlers(tabsEl, (tab) => renderTimeline(tab));

    // Filter
    const fb = container().querySelector('.filter-bar');
    if (fb) {
      const input = fb.querySelector('.filter-search');
      input?.addEventListener('input', () => {
        const search = input.value.toLowerCase();
        const items = container().querySelectorAll('.audit-event');
        items.forEach(item => {
          item.style.display = !search || item.textContent.toLowerCase().includes(search) ? '' : 'none';
        });
      });
    }
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load events: ' + e.message);
  }
}
