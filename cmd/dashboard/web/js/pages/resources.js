import { $ } from '../utils.js';
import { destroyCharts } from '../charts.js';
import { renderNodes } from './nodes.js';
import { renderWorkloads } from './workloads.js';
import { renderRecsTab } from './recommendations.js';
import { renderActions } from './actions.js';

const container = () => $('#page-container');

const tabDefs = [
  { id: 'nodes', label: 'Nodes' },
  { id: 'workloads', label: 'Workloads' },
  { id: 'recommendations', label: 'Recommendations' },
  { id: 'actions', label: 'Actions' },
];

const renderers = {
  nodes: renderNodes,
  workloads: renderWorkloads,
  recommendations: renderRecsTab,
  actions: renderActions,
};

export async function renderResources(params) {
  const activeTab = params?.tab || 'nodes';

  container().innerHTML = `
    <div class="page-header"><h1>Resources</h1><p>Nodes, workloads, and optimization recommendations</p></div>
    <div class="tabs" id="resources-tabs">
      ${tabDefs.map(t => `<button class="tab ${t.id === activeTab ? 'tab-active' : ''}" data-tab="${t.id}">${t.label}</button>`).join('')}
    </div>
    <div id="resources-content"></div>`;

  const contentEl = document.getElementById('resources-content');

  async function switchTab(tabId) {
    destroyCharts();
    contentEl.innerHTML = '';
    const render = renderers[tabId];
    if (render) await render(contentEl);
  }

  document.getElementById('resources-tabs').addEventListener('click', (e) => {
    const btn = e.target.closest('.tab');
    if (!btn) return;
    const tabId = btn.dataset.tab;
    document.querySelectorAll('#resources-tabs .tab').forEach(b => b.classList.remove('tab-active'));
    btn.classList.add('tab-active');
    history.replaceState(null, '', tabId === 'nodes' ? '#/resources' : `#/resources/${tabId}`);
    switchTab(tabId);
  });

  await switchTab(activeTab);
}
