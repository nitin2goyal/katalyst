import { $ } from '../utils.js';
import { destroyCharts } from '../charts.js';
import { renderGPU } from './gpu.js';
import { renderCommitments } from './commitments.js';
import { renderSpot } from './spot.js';

const container = () => $('#page-container');

const tabDefs = [
  { id: 'gpu', label: 'GPU' },
  { id: 'commitments', label: 'Commitments' },
  { id: 'spot', label: 'Spot' },
];

const renderers = {
  gpu: renderGPU,
  commitments: renderCommitments,
  spot: renderSpot,
};

export async function renderInfrastructure(params) {
  const activeTab = params?.tab || 'gpu';

  container().innerHTML = `
    <div class="page-header"><h1>Infrastructure</h1><p>GPU resources and commitment management</p></div>
    <div class="tabs" id="infra-tabs">
      ${tabDefs.map(t => `<button class="tab ${t.id === activeTab ? 'tab-active' : ''}" data-tab="${t.id}">${t.label}</button>`).join('')}
    </div>
    <div id="infra-content"></div>`;

  const contentEl = document.getElementById('infra-content');

  async function switchTab(tabId) {
    destroyCharts();
    contentEl.innerHTML = '';
    const render = renderers[tabId];
    if (render) await render(contentEl);
  }

  // Tab click handlers
  document.getElementById('infra-tabs').addEventListener('click', (e) => {
    const btn = e.target.closest('.tab');
    if (!btn) return;
    const tabId = btn.dataset.tab;
    document.querySelectorAll('#infra-tabs .tab').forEach(b => b.classList.remove('tab-active'));
    btn.classList.add('tab-active');
    history.replaceState(null, '', `#/infrastructure/${tabId}`);
    switchTab(tabId);
  });

  await switchTab(activeTab);
}
