// Katalyst Dashboard - ES Module Orchestrator
import { api, apiPut, auditAction } from './api.js';
import { $ } from './utils.js';
import { store } from './store.js';
import { addRoute, initRouter, handleNavigation } from './router.js';
import { checkAuth, showLogin } from './auth.js';

// Pages
import { renderOverview } from './pages/overview.js';
import { renderCost } from './pages/cost.js';
import { renderResources } from './pages/resources.js';
import { renderNodeDetail } from './pages/node-detail.js';
import { renderNodeGroupDetail } from './pages/nodegroup-detail.js';
import { renderWorkloadDetail } from './pages/workload-detail.js';
import { renderSettings } from './pages/settings.js';
import { renderInfrastructure } from './pages/infrastructure.js';
import { renderAutoscaler } from './pages/autoscaler.js';
import { renderScaleDown } from './pages/scaledown.js';
import { renderHelmDrift } from './pages/helmdrift.js';
import { renderInefficiency } from './pages/inefficiency.js';

// Backward-compat redirects for old routes
const redirectMap = {
  '/savings': '#/cost/savings',
  '/impact': '#/cost',
  '/allocation': '#/cost',
  '/network-cost': '#/cost',
  '/nodes': '#/resources',
  '/workloads': '#/resources/workloads',
  '/recommendations': '#/resources/recommendations',
  '/idle-resources': '#/resources/recommendations',
  '/gpu': '#/infrastructure/gpu',
  '/commitments': '#/infrastructure/commitments',
  '/multi-cluster': '#/infrastructure',
  '/events': '#/settings',
  '/audit': '#/settings',
  '/activity': '#/settings',
  '/notifications': '#/settings',
  '/policies': '#/settings',
};

// Register routes (specific before parameterized)
addRoute('/overview', renderOverview);
addRoute('/cost', (params) => renderCost(params));
addRoute('/cost/{tab}', (params) => renderCost(params));
addRoute('/resources', (params) => renderResources(params));
addRoute('/resources/{tab}', (params) => renderResources(params));
addRoute('/nodes/{name}', renderNodeDetail);
addRoute('/nodegroups/{id}', renderNodeGroupDetail);
addRoute('/workloads/{ns}/{kind}/{name}', renderWorkloadDetail);
addRoute('/infrastructure', (params) => renderInfrastructure(params));
addRoute('/infrastructure/{tab}', (params) => renderInfrastructure(params));
addRoute('/autoscaler', (params) => renderAutoscaler(params));
addRoute('/autoscaler/{tab}', (params) => renderAutoscaler(params));
addRoute('/scaledown', (params) => renderScaleDown(params));
addRoute('/scaledown/{tab}', (params) => renderScaleDown(params));
addRoute('/inefficiency', (params) => renderInefficiency(params));
addRoute('/inefficiency/{tab}', (params) => renderInefficiency(params));
addRoute('/helm-drift', renderHelmDrift);
addRoute('/settings', renderSettings);

// Backward-compat redirects for old routes
for (const [oldPath, target] of Object.entries(redirectMap)) {
  addRoute(oldPath, () => { location.hash = target; });
}

// Mode badge
async function updateModeBadge() {
  try {
    const cfg = await api('/config');
    const mode = (cfg && cfg.mode) || 'unknown';
    const el = $('#mode-badge');
    if (el) {
      el.textContent = mode.charAt(0).toUpperCase() + mode.slice(1);
      el.className = 'mode-badge ' + mode;
    }
  } catch (_) {}
}

// Mode badge click to toggle between recommend and active
document.getElementById('mode-badge')?.addEventListener('click', async () => {
  const el = $('#mode-badge');
  if (!el) return;
  const current = el.textContent.trim().toLowerCase();
  const newMode = current === 'active' ? 'recommend' : 'active';
  try {
    await apiPut('/config/mode', { mode: newMode });
    auditAction('mode.changed', 'cluster', `Mode switched from ${current} to ${newMode}`);
    el.textContent = newMode.charAt(0).toUpperCase() + newMode.slice(1);
    el.className = 'mode-badge ' + newMode;
    store.clear();
    handleNavigation();
  } catch (_) {}
});

// Sidebar theme toggle click
document.getElementById('theme-toggle')?.addEventListener('click', () => {
  window.dispatchEvent(new CustomEvent('kopt-theme-toggle'));
});

// Manual refresh
let lastUpdated = Date.now();
function updateRefreshIndicator() {
  const el = $('#refresh-age');
  if (!el) return;
  const secs = Math.floor((Date.now() - lastUpdated) / 1000);
  el.textContent = secs < 5 ? 'Just now' : `${secs}s ago`;
}

function doRefresh() {
  store.clear();
  lastUpdated = Date.now();
  handleNavigation();
  updateModeBadge();
  updateRefreshIndicator();
}

document.getElementById('refresh-btn')?.addEventListener('click', doRefresh);

// Dark mode - dark is default (no attribute), light mode uses data-theme="light"
function initTheme() {
  const saved = localStorage.getItem('kopt-theme');
  if (saved === 'light') {
    document.documentElement.setAttribute('data-theme', 'light');
    const btn = $('#theme-toggle');
    if (btn) btn.title = 'Switch to dark mode';
    const icon = btn?.querySelector('.theme-icon');
    if (icon) icon.textContent = '\u263E';
  }
  // Dark is default - no attribute needed
}

window.addEventListener('kopt-theme-toggle', () => {
  const isLight = document.documentElement.getAttribute('data-theme') === 'light';
  if (isLight) {
    // Switch to dark (default)
    document.documentElement.removeAttribute('data-theme');
    localStorage.setItem('kopt-theme', 'dark');
    const btn = $('#theme-toggle');
    if (btn) btn.title = 'Switch to light mode';
    const icon = btn?.querySelector('.theme-icon');
    if (icon) icon.textContent = '\u2600';
  } else {
    // Switch to light
    document.documentElement.setAttribute('data-theme', 'light');
    localStorage.setItem('kopt-theme', 'light');
    const btn = $('#theme-toggle');
    if (btn) btn.title = 'Switch to dark mode';
    const icon = btn?.querySelector('.theme-icon');
    if (icon) icon.textContent = '\u263E';
  }
});

// Refresh indicator update (single global interval — safe since this module loads once)
const _refreshInterval = setInterval(updateRefreshIndicator, 5000);

// Sidebar collapse toggle
function initSidebarCollapse() {
  const saved = localStorage.getItem('kopt-sidebar-collapsed');
  if (saved === 'true') {
    document.getElementById('sidebar')?.classList.add('collapsed');
    document.body.classList.add('sidebar-collapsed');
    const btn = document.getElementById('sidebar-collapse');
    if (btn) btn.title = 'Expand sidebar';
  }
}

document.querySelector('.sidebar-header')?.addEventListener('click', () => {
  const sidebar = document.getElementById('sidebar');
  if (!sidebar) return;
  const collapsed = sidebar.classList.toggle('collapsed');
  document.body.classList.toggle('sidebar-collapsed', collapsed);
  localStorage.setItem('kopt-sidebar-collapsed', String(collapsed));
});

// ── Global event delegation ────────────────────────────────────────────
// Eliminates all inline onclick handlers, enabling strict CSP (no unsafe-inline).

// Export registry for CSV export buttons: data-export="key" triggers registered fn.
const _exportHandlers = new Map();
export function registerExport(key, fn) { _exportHandlers.set(key, fn); }
export function unregisterExport(key) { _exportHandlers.delete(key); }

// Action registry for custom actions: data-action-key="key" triggers registered fn.
const _actionHandlers = new Map();
export function registerAction(key, fn) { _actionHandlers.set(key, fn); }
export function unregisterAction(key) { _actionHandlers.delete(key); }

document.addEventListener('click', (e) => {
  // 1. Clickable rows: data-href="#/path" → navigate
  const hrefEl = e.target.closest('[data-href]');
  if (hrefEl) { location.hash = hrefEl.dataset.href; return; }

  // 2. Export CSV: data-export="key" → call registered export handler
  const exportEl = e.target.closest('[data-export]');
  if (exportEl) { _exportHandlers.get(exportEl.dataset.export)?.(); return; }

  // 3. Modal overlay dismiss: click on overlay background
  if (e.target.classList.contains('modal-overlay')) { e.target.remove(); return; }

  // 4. Modal close button
  const closeBtn = e.target.closest('.modal-close');
  if (closeBtn) { closeBtn.closest('.modal-overlay')?.remove(); return; }

  // 5. JSON toggle: .event-json-toggle → toggle next sibling
  const jsonToggle = e.target.closest('.event-json-toggle');
  if (jsonToggle) {
    const pre = jsonToggle.nextElementSibling;
    if (pre) pre.style.display = pre.style.display === 'none' ? 'block' : 'none';
    return;
  }

  // 6. Custom actions: data-action-key="key" → call registered handler (passes element)
  const actionEl = e.target.closest('[data-action-key]');
  if (actionEl) { _actionHandlers.get(actionEl.dataset.actionKey)?.(actionEl); return; }
});

// Init — check auth before loading the app
async function init() {
  const authed = await checkAuth();
  if (!authed) {
    showLogin(() => init());
    return;
  }
  document.getElementById('sidebar').style.display = '';
  document.getElementById('content').style.display = '';
  initSidebarCollapse();
  initTheme();
  updateModeBadge();
  initRouter();
  lastUpdated = Date.now();
}

init();
