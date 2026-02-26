// KOptimizer Dashboard - ES Module Orchestrator
import { api, apiPut } from './api.js';
import { $ } from './utils.js';
import { store } from './store.js';
import { addRoute, initRouter, handleNavigation } from './router.js';

// Pages
import { renderOverview } from './pages/overview.js';
import { renderCost } from './pages/cost.js';
import { renderResources } from './pages/resources.js';
import { renderNodeDetail } from './pages/node-detail.js';
import { renderNodeGroupDetail } from './pages/nodegroup-detail.js';
import { renderWorkloadDetail } from './pages/workload-detail.js';
import { renderSettings } from './pages/settings.js';
import { renderInfrastructure } from './pages/infrastructure.js';

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

// Mode badge click to toggle between recommend and enforce
document.getElementById('mode-badge')?.addEventListener('click', async () => {
  const el = $('#mode-badge');
  if (!el) return;
  const current = el.textContent.trim().toLowerCase();
  const newMode = current === 'recommend' ? 'enforce' : 'recommend';
  try {
    await apiPut('/config/mode', { mode: newMode });
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

// Refresh indicator update
setInterval(updateRefreshIndicator, 5000);

// Init
initTheme();
updateModeBadge();
initRouter();
lastUpdated = Date.now();
