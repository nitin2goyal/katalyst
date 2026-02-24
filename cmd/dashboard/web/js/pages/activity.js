import { $ } from '../utils.js';
import { destroyCharts } from '../charts.js';
import { renderEvents } from './events.js';
import { renderAudit } from './audit.js';

const container = () => $('#page-container');

const tabDefs = [
  { id: 'events', label: 'Events' },
  { id: 'audit', label: 'Audit Log' },
];

const renderers = {
  events: renderEvents,
  audit: renderAudit,
};

export async function renderActivity(params) {
  const activeTab = params?.tab || 'events';

  container().innerHTML = `
    <div class="page-header"><h1>Activity</h1><p>Event log and audit trail</p></div>
    <div class="tabs" id="activity-tabs">
      ${tabDefs.map(t => `<button class="tab ${t.id === activeTab ? 'tab-active' : ''}" data-tab="${t.id}">${t.label}</button>`).join('')}
    </div>
    <div id="activity-content"></div>`;

  const contentEl = document.getElementById('activity-content');

  async function switchTab(tabId) {
    destroyCharts();
    contentEl.innerHTML = '';
    const render = renderers[tabId];
    if (render) await render(contentEl);
  }

  // Tab click handlers
  document.getElementById('activity-tabs').addEventListener('click', (e) => {
    const btn = e.target.closest('.tab');
    if (!btn) return;
    const tabId = btn.dataset.tab;
    document.querySelectorAll('#activity-tabs .tab').forEach(b => b.classList.remove('tab-active'));
    btn.classList.add('tab-active');
    // Update URL without re-rendering the whole page
    history.replaceState(null, '', `#/activity/${tabId}`);
    switchTab(tabId);
  });

  await switchTab(activeTab);
}
