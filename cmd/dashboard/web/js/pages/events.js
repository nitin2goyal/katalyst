import { api, apiPost } from '../api.js';
import { $, toArray, timeAgo, errorMsg, escapeHtml } from '../utils.js';
import { skeleton, tabs, attachTabHandlers, filterBar, badge, cardHeader, toast } from '../components.js';
import { addCleanup } from '../router.js';

const eventColor = (action) => {
  if (!action) return 'gray';
  if (action.includes('approved') || action.includes('scaled') || action.includes('rightsized')) return 'green';
  if (action.includes('dismissed') || action.includes('error')) return 'red';
  if (action.includes('warning') || action.includes('alert')) return 'amber';
  if (action.includes('config') || action.includes('mode')) return 'purple';
  if (action.includes('dry-run')) return 'blue';
  return 'blue';
};

const eventCategory = (action) => {
  if (!action) return 'other';
  if (action.includes('recommendation') || action.includes('rightsiz') || action.includes('savings') || action.includes('eviction') || action.includes('dry-run')) return 'optimization';
  if (action.includes('scale') || action.includes('node')) return 'scaling';
  if (action.includes('config') || action.includes('mode')) return 'configuration';
  if (action.includes('alert') || action.includes('security')) return 'security';
  return 'other';
};

const isActionable = (action) => {
  if (!action) return false;
  return action.includes('dry-run') || action.includes('recommendation');
};

export async function renderEvents(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const [data, cfg] = await Promise.all([
      api('/events?pageSize=1000').catch(() => api('/audit').catch(() => [])),
      api('/config').catch(() => ({})),
    ]);
    const mode = ((cfg && cfg.mode) || 'recommend').toUpperCase();
    // Filter out spot-related events (spot feature removed)
    const events = toArray(data, 'events').filter(e => {
      const action = (e.action || '').toLowerCase();
      return !action.includes('spot');
    });
    const today = new Date().toDateString();
    const todayEvents = events.filter(e => new Date(e.timestamp).toDateString() === today);
    const actionableEvents = events.filter(e => isActionable(e.action));

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
        <div class="kpi-card"><div class="label">Actionable</div><div class="value amber">${actionableEvents.length}</div></div>
        <div class="kpi-card"><div class="label">Mode</div><div class="value">${mode}</div><div class="sub">${mode === 'RECOMMEND' ? 'dry-run only' : 'active'}</div></div>
      </div>
      <div class="card">
        ${cardHeader('Activity Stream')}
        ${tabs(catTabs, 'all')}
        ${filterBar({ placeholder: 'Search events...' })}
        <div class="audit-timeline" id="events-timeline"></div>
      </div>`;

    const PAGE_SIZE = targetEl ? 25 : 50;
    let currentPage = 0;

    function renderTimeline(category) {
      currentPage = 0;
      const filtered = category === 'all' ? events : events.filter(e => eventCategory(e.action) === category);
      const timeline = $('#events-timeline');

      function renderPage() {
        const start = currentPage * PAGE_SIZE;
        const pageItems = filtered.slice(start, start + PAGE_SIZE);
        const totalPages = Math.ceil(filtered.length / PAGE_SIZE);

        timeline.innerHTML = pageItems.length ? pageItems.map((e, i) => {
          const idx = start + i;
          const color = eventColor(e.action);
          const actionable = isActionable(e.action);
          const applyBtn = actionable
            ? `<button class="btn btn-green btn-sm" data-apply-idx="${idx}" onclick="window.__applyEvent(${idx})">Apply</button>`
            : '';
          return `<div class="audit-event" data-category="${eventCategory(e.action)}">
            <div class="audit-event-dot ${color}"></div>
            <div class="audit-event-content">
              <div class="audit-event-header">
                ${badge(e.action || 'event', color)}
                ${actionable ? badge('actionable', 'amber') : ''}
                <span class="audit-event-time">${timeAgo(e.timestamp)}</span>
              </div>
              <div class="audit-event-details">
                <span class="audit-event-target">${escapeHtml(e.target || '')}</span>
                <span class="audit-event-desc">${escapeHtml(e.details || '')}</span>
              </div>
              <div class="audit-event-meta">
                <span>User: ${e.user || 'system'}</span>
                <span>${e.timestamp ? new Date(e.timestamp).toLocaleString() : ''}</span>
                ${applyBtn}
                <button class="btn btn-gray btn-sm event-json-toggle" onclick="this.nextElementSibling.style.display=this.nextElementSibling.style.display==='none'?'block':'none'">JSON</button>
                <pre class="event-json" style="display:none">${escapeHtml(JSON.stringify(e, null, 2))}</pre>
              </div>
            </div>
          </div>`;
        }).join('') : '<div style="color:var(--text-muted);padding:24px;text-align:center">No events in this category</div>';

        if (totalPages > 1) {
          timeline.insertAdjacentHTML('beforeend', `<div style="display:flex;justify-content:center;align-items:center;gap:12px;padding:16px 0;border-top:1px solid var(--border);margin-top:8px">
            <button class="btn btn-gray btn-sm" id="evt-prev" ${currentPage === 0 ? 'disabled' : ''}>Previous</button>
            <span style="font-size:13px;color:var(--text-muted)">Page ${currentPage + 1} of ${totalPages} (${filtered.length} events)</span>
            <button class="btn btn-gray btn-sm" id="evt-next" ${currentPage >= totalPages - 1 ? 'disabled' : ''}>Next</button>
          </div>`);
          document.getElementById('evt-prev')?.addEventListener('click', () => { if (currentPage > 0) { currentPage--; renderPage(); } });
          document.getElementById('evt-next')?.addEventListener('click', () => { if (currentPage < totalPages - 1) { currentPage++; renderPage(); } });
        }
      }
      renderPage();
    }

    window.__applyEvent = async function (idx) {
      const e = events[idx];
      if (!e) return;
      // For dry-run-eviction events, attempt to apply via mode switch
      if (e.action === 'dry-run-eviction') {
        toast('Dry-run eviction for ' + (e.target || 'node') + '. Switch to OPTIMIZE mode via Settings to enable automatic execution.', 'info');
        return;
      }
      toast('Event action noted. Switch to OPTIMIZE mode to enable automatic execution.', 'info');
    };
    addCleanup(() => { delete window.__applyEvent; });

    renderTimeline('all');

    // Tab handling
    const tabsEl = container().querySelector('.tabs');
    if (tabsEl) attachTabHandlers(tabsEl, (tab) => renderTimeline(tab));

    // Filter
    let filterDebounce;
    const fb = container().querySelector('.filter-bar');
    if (fb) {
      const input = fb.querySelector('.filter-search');
      const onInput = () => {
        clearTimeout(filterDebounce);
        filterDebounce = setTimeout(() => {
          const search = input.value.toLowerCase();
          const items = container().querySelectorAll('.audit-event');
          items.forEach(item => {
            item.style.display = !search || item.textContent.toLowerCase().includes(search) ? '' : 'none';
          });
        }, 300);
      };
      input?.addEventListener('input', onInput);
    }

    // Register cleanup
    const cleanup = () => {
      clearTimeout(filterDebounce);
      delete window.__applyEvent;
    };
    addCleanup(cleanup);
    return cleanup;
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load events: ' + e.message);
  }
}
