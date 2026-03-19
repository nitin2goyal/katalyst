import { api } from '../api.js';
import { $, badge, escapeHtml, timeAgo, errorMsg, fmt$, fmtPct } from '../utils.js';
import { skeleton, makeSortable, attachPagination, cardHeader, filterBar, attachFilterHandlers } from '../components.js';
import { addCleanup } from '../router.js';

const container = () => $('#page-container');

const tabDefs = [
  { id: 'overview', label: 'Exec Summary' },
  { id: 'maxpods', label: 'Max Pods' },
  { id: 'antiaffinity', label: 'Anti-Affinity' },
  { id: 'badratio', label: 'Bad Ratio' },
  { id: 'keda', label: 'Autoscaler Issues' },
  { id: 'pdb', label: 'Bad PDBs' },
  { id: 'pods', label: 'Bad Pods' },
  { id: 'fragmentation', label: 'Fragmentation' },
  { id: 'network', label: 'Network I/O' },
];

const subRenderers = {
  overview: renderOverview,
  maxpods: renderMaxPods,
  antiaffinity: renderAntiAffinity,
  badratio: renderBadRatio,
  keda: renderKEDA,
  pdb: renderPDB,
  pods: renderBadPods,
  fragmentation: renderFragmentation,
  network: renderNetwork,
};

export async function renderInefficiency(params) {
  const activeTab = params?.tab || 'overview';

  container().innerHTML = `
    <div class="page-header">
      <h1>Cluster Inefficiencies</h1>
      <p>Consolidated view of all cluster-level issues preventing optimal scaling, allocation, and cost efficiency</p>
    </div>
    <div class="tabs" id="page-tabs">
      ${tabDefs.map(t => `<button class="tab ${t.id === activeTab ? 'tab-active' : ''}" data-tab="${t.id}">${t.label}</button>`).join('')}
    </div>
    <div id="content-area"></div>`;

  const contentEl = document.getElementById('content-area');

  async function switchTab(tabId) {
    contentEl.innerHTML = '';
    const render = subRenderers[tabId];
    if (render) await render(contentEl);
  }

  const tabHandler = (e) => {
    const btn = e.target.closest('.tab');
    if (!btn) return;
    const tabId = btn.dataset.tab;
    document.querySelectorAll('#page-tabs .tab').forEach(b => b.classList.remove('tab-active'));
    btn.classList.add('tab-active');
    history.replaceState(null, '', tabId === 'overview' ? '#/inefficiency' : `#/inefficiency/${tabId}`);
    switchTab(tabId);
  };
  document.getElementById('page-tabs').addEventListener('click', tabHandler);
  addCleanup(() => document.getElementById('page-tabs')?.removeEventListener('click', tabHandler));

  await switchTab(activeTab);
}

// --- Helpers ---

function sevBadge(sev) {
  const colors = { critical: 'red', warning: 'amber', info: 'gray' };
  return badge(sev, colors[sev] || 'gray');
}

function sevKPI(count, label, color) {
  if (count === 0) return `<div class="kpi-card"><div class="label">${label}</div><div class="value">${count}</div></div>`;
  return `<div class="kpi-card"><div class="label">${label}</div><div class="value ${color}">${count}</div></div>`;
}

function shortNode(name) {
  const parts = name.split('-');
  if (parts.length > 4) return '...' + parts.slice(-3).join('-');
  return name;
}

const categoryLabels = {
  maxPods: 'Max Pods per Node',
  antiAffinity: 'Anti-Affinity Spread',
  badRatio: 'Bad Resource Ratio',
  keda: 'Autoscaler / KEDA Issues',
  badPDBs: 'Bad PDBs',
  badPods: 'Bad State Pods',
  fragmentation: 'Node Fragmentation',
  networkHogs: 'Network I/O Hogs',
};

const categoryIcons = {
  maxPods: '&#x26A0;',
  antiAffinity: '&#x2194;',
  badRatio: '&#x2696;',
  keda: '&#x26A1;',
  badPDBs: '&#x1F6E1;',
  badPods: '&#x2620;',
  fragmentation: '&#x1F9E9;',
  networkHogs: '&#x1F4E1;',
};

const categoryTabs = {
  maxPods: 'maxpods',
  antiAffinity: 'antiaffinity',
  badRatio: 'badratio',
  keda: 'keda',
  badPDBs: 'pdb',
  badPods: 'pods',
  fragmentation: 'fragmentation',
  networkHogs: 'network',
};

// --- Exec Summary Tab ---

async function renderOverview(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/inefficiencies');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load inefficiencies: ' + err.message);
    return;
  }

  const s = data.summary || {};
  const cats = s.categories || [];

  // Find categories with issues, sorted by severity
  const activeCats = cats.filter(c => c.count > 0).sort((a, b) => {
    const sevOrder = { critical: 3, warning: 2, info: 1, '': 0 };
    return (sevOrder[b.severity] || 0) - (sevOrder[a.severity] || 0);
  });

  targetEl.innerHTML = `
    <div class="kpi-grid">
      ${sevKPI(s.totalIssues || 0, 'Total Issues', s.totalIssues > 0 ? 'amber' : '')}
      ${sevKPI(s.criticalCount || 0, 'Critical', 'red')}
      ${sevKPI(s.warningCount || 0, 'Warning', 'amber')}
      ${sevKPI(s.infoCount || 0, 'Info', '')}
      <div class="kpi-card"><div class="label">Est. Wasted Cost</div><div class="value ${s.estimatedWastedMonthlyCost > 100 ? 'red' : ''}">${fmt$(s.estimatedWastedMonthlyCost || 0)}/mo</div></div>
    </div>

    ${s.totalIssues === 0 ? `
      <div class="card">
        <div class="empty-state-center" style="padding:3rem">
          <div style="font-size:48px;margin-bottom:1rem">&#x2705;</div>
          <h2 style="margin:0 0 0.5rem">No Inefficiencies Detected</h2>
          <p style="color:var(--text-muted)">Your cluster is running efficiently. All checks passed.</p>
        </div>
      </div>` : `
      <div class="card">
        ${cardHeader('Issue Breakdown')}
        <div style="padding:1rem">
          ${activeCats.map(c => {
            const label = categoryLabels[c.category] || c.category;
            const icon = categoryIcons[c.category] || '&#x2022;';
            const tab = categoryTabs[c.category] || 'overview';
            const sevColor = c.severity === 'critical' ? 'var(--red)' : c.severity === 'warning' ? 'var(--amber)' : 'var(--text-muted)';
            return `
              <div class="bar-row" style="cursor:pointer" data-href="#/inefficiency/${tab}">
                <span class="bar-label" style="min-width:220px">
                  <span style="margin-right:6px">${icon}</span> ${label}
                </span>
                <div class="bar-track" style="flex:1">
                  <div class="bar-fill" style="width:${Math.min(100, c.count / (s.totalIssues || 1) * 100)}%;background:${sevColor}">
                    <span class="bar-fill-text">${c.count}</span>
                  </div>
                </div>
                <span style="margin-left:8px">${sevBadge(c.severity)}</span>
              </div>`;
          }).join('')}
        </div>
      </div>

      ${renderTopIssues(data)}
    `}
  `;
}

function renderTopIssues(data) {
  // Collect top issues from all categories
  const issues = [];

  for (const item of (data.maxPods || []).slice(0, 3)) {
    issues.push({ category: 'Max Pods', severity: item.severity, target: item.nodeName, impact: item.impact });
  }
  for (const item of (data.antiAffinity || []).slice(0, 3)) {
    issues.push({ category: 'Anti-Affinity', severity: item.severity, target: `${item.namespace}/${item.name}`, impact: item.impact });
  }
  for (const item of (data.keda || []).slice(0, 3)) {
    issues.push({ category: 'Autoscaler', severity: item.severity, target: `${item.namespace}/${item.name}`, impact: item.impact });
  }
  for (const item of (data.badPDBs || []).slice(0, 3)) {
    issues.push({ category: 'Bad PDB', severity: item.severity, target: `${item.namespace}/${item.name}`, impact: item.impact });
  }
  for (const item of (data.badPods || []).slice(0, 3)) {
    issues.push({ category: 'Bad Pod', severity: item.severity, target: `${item.namespace}/${item.name}`, impact: item.impact });
  }
  for (const item of (data.badRatio || []).slice(0, 3)) {
    issues.push({ category: 'Bad Ratio', severity: item.severity, target: `${item.namespace}/${item.name}`, impact: item.impact });
  }
  for (const item of (data.fragmentation || []).slice(0, 3)) {
    issues.push({ category: 'Fragmentation', severity: item.severity, target: item.nodeName, impact: item.impact });
  }
  for (const item of (data.networkHogs || []).slice(0, 3)) {
    issues.push({ category: 'Network I/O', severity: item.severity, target: `${item.namespace}/${item.name}`, impact: item.impact });
  }

  // Sort by severity
  const sevOrder = { critical: 3, warning: 2, info: 1 };
  issues.sort((a, b) => (sevOrder[b.severity] || 0) - (sevOrder[a.severity] || 0));

  const top = issues.slice(0, 15);
  if (top.length === 0) return '';

  return `
    <div class="card">
      ${cardHeader('Top Issues (by severity)')}
      <div class="table-wrap"><table id="top-issues-table">
        <thead><tr>
          <th>Severity</th>
          <th>Category</th>
          <th>Target</th>
          <th>Impact</th>
        </tr></thead>
        <tbody>
          ${top.map(i => `<tr>
            <td>${sevBadge(i.severity)}</td>
            <td>${escapeHtml(i.category)}</td>
            <td class="cell-truncate" title="${escapeHtml(i.target)}">${escapeHtml(i.target)}</td>
            <td style="max-width:500px;font-size:12px">${escapeHtml(i.impact)}</td>
          </tr>`).join('')}
        </tbody>
      </table></div>
    </div>`;
}

// --- Max Pods Tab ---

async function renderMaxPods(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/inefficiencies');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const items = data.maxPods || [];

  targetEl.innerHTML = `
    ${items.length > 0 ? `
    <div class="card alert-border-amber mb-4">
      <div class="alert-card">
        <span class="alert-card-icon">&#9888;</span>
        <div>
          <strong>${items.length} node${items.length > 1 ? 's' : ''} hitting max pods limit</strong>
          <div class="alert-card-sub">
            These nodes are at or near their maxPods limit but have significant unused CPU/memory capacity.
            Pods cannot be scheduled even though resources are available. Consider increasing maxPods in the node group configuration (e.g., --max-pods-per-node for GKE, maxPods in kubelet config).
          </div>
        </div>
      </div>
    </div>` : ''}

    <div class="card">
      ${cardHeader('Nodes at Max Pods Limit (' + items.length + ')')}
      ${items.length === 0
        ? '<div class="empty-state-center text-small-muted">No nodes hitting max pods limit</div>'
        : `<div class="table-wrap"><table id="maxpods-table">
          <thead><tr>
            <th>Node</th>
            <th>Instance Type</th>
            <th>Pods (current/max)</th>
            <th>Pod %</th>
            <th>CPU Alloc %</th>
            <th>Mem Alloc %</th>
            <th>CPU Usage %</th>
            <th>Wasted CPU</th>
            <th>Wasted Mem</th>
            <th>Cost/mo</th>
            <th>Severity</th>
          </tr></thead>
          <tbody>
            ${items.map(i => `<tr>
              <td><a href="#/nodes/${encodeURIComponent(i.nodeName)}">${escapeHtml(shortNode(i.nodeName))}</a></td>
              <td>${escapeHtml(i.instanceType || '-')}</td>
              <td>${i.currentPods}/${i.maxPods}</td>
              <td>${fmtPct(i.podUtilizationPct / 100)}</td>
              <td>${fmtPct(i.cpuAllocationPct / 100)}</td>
              <td>${fmtPct(i.memAllocationPct / 100)}</td>
              <td>${fmtPct(i.cpuUsagePct / 100)}</td>
              <td>${i.wastedCPUCores} cores</td>
              <td>${i.wastedMemGB} GB</td>
              <td>${fmt$(i.monthlyCostUSD)}</td>
              <td>${sevBadge(i.severity)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (items.length > 0) {
    const table = $('#maxpods-table');
    makeSortable(table);
    attachPagination(table);
  }
}

// --- Anti-Affinity Tab ---

async function renderAntiAffinity(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/inefficiencies');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const items = data.antiAffinity || [];

  targetEl.innerHTML = `
    ${items.length > 0 ? `
    <div class="card alert-border-amber mb-4">
      <div class="alert-card">
        <span class="alert-card-icon">&#9888;</span>
        <div>
          <strong>${items.length} workload${items.length > 1 ? 's' : ''} with anti-affinity causing poor allocation</strong>
          <div class="alert-card-sub">
            These workloads use pod anti-affinity rules that force pods onto separate nodes.
            This causes underutilized nodes that can't be consolidated. Consider relaxing anti-affinity to preferred (soft) rules, or using topology spread constraints with maxSkew > 1.
          </div>
        </div>
      </div>
    </div>` : ''}

    <div class="card">
      ${cardHeader('Anti-Affinity Spread Issues (' + items.length + ')')}
      ${items.length === 0
        ? '<div class="empty-state-center text-small-muted">No anti-affinity spread issues found</div>'
        : `<div class="table-wrap"><table id="antiaffinity-table">
          <thead><tr>
            <th>Workload</th>
            <th>Namespace</th>
            <th>Replicas</th>
            <th>Nodes Used</th>
            <th>Affinity</th>
            <th>Avg CPU Alloc</th>
            <th>Avg Mem Alloc</th>
            <th>Nodes Wasted</th>
            <th>Wasted Cost/mo</th>
            <th>Severity</th>
          </tr></thead>
          <tbody>
            ${items.map(i => `<tr>
              <td><a href="#/workloads/${encodeURIComponent(i.namespace)}/${encodeURIComponent(i.kind)}/${encodeURIComponent(i.name)}">${escapeHtml(i.name)}</a></td>
              <td>${escapeHtml(i.namespace)}</td>
              <td>${i.replicas}</td>
              <td>${i.nodeCount}</td>
              <td>${badge(i.affinityType, i.affinityType === 'required' ? 'red' : 'amber')}</td>
              <td>${fmtPct(i.avgNodeCPUAllocationPct / 100)}</td>
              <td>${fmtPct(i.avgNodeMemAllocationPct / 100)}</td>
              <td>${i.nodesEffectivelyWasted}</td>
              <td>${fmt$(i.wastedMonthlyCostUSD)}</td>
              <td>${sevBadge(i.severity)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (items.length > 0) {
    const table = $('#antiaffinity-table');
    makeSortable(table);
    attachPagination(table);
  }
}

// --- Bad Ratio Tab ---

async function renderBadRatio(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/inefficiencies');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const items = data.badRatio || [];

  targetEl.innerHTML = `
    ${items.length > 0 ? `
    <div class="card alert-border-amber mb-4">
      <div class="alert-card">
        <span class="alert-card-icon">&#9888;</span>
        <div>
          <strong>${items.length} workload${items.length > 1 ? 's' : ''} with mismatched CPU:memory ratio</strong>
          <div class="alert-card-sub">
            These workloads request resources at a very different CPU:memory ratio than their node group provides.
            For example, a workload requesting 1 CPU : 4 GB on a 1 CPU : 13 GB node wastes memory because CPU fills up before memory does.
            Consider adjusting requests to match the node ratio, or moving workloads to node groups with a matching ratio.
          </div>
        </div>
      </div>
    </div>` : ''}

    <div class="card">
      ${cardHeader('Bad Resource Ratio Workloads (' + items.length + ')')}
      ${items.length === 0
        ? '<div class="empty-state-center text-small-muted">No bad ratio workloads found</div>'
        : `<div class="table-wrap"><table id="badratio-table">
          <thead><tr>
            <th>Workload</th>
            <th>Namespace</th>
            <th>Replicas</th>
            <th>CPU/pod</th>
            <th>Mem/pod</th>
            <th>Workload Ratio</th>
            <th>Node Ratio</th>
            <th>Deviation</th>
            <th>Wasted</th>
            <th>Cost Impact/mo</th>
            <th>Severity</th>
          </tr></thead>
          <tbody>
            ${items.map(i => `<tr>
              <td><a href="#/workloads/${encodeURIComponent(i.namespace)}/${encodeURIComponent(i.kind)}/${encodeURIComponent(i.name)}">${escapeHtml(i.name)}</a></td>
              <td>${escapeHtml(i.namespace)}</td>
              <td>${i.replicas}</td>
              <td>${escapeHtml(i.cpuRequestPerPod)}</td>
              <td>${escapeHtml(i.memRequestPerPod)}</td>
              <td>${i.workloadRatioGBPerCPU} GB/CPU</td>
              <td>${i.nodeRatioGBPerCPU} GB/CPU</td>
              <td>${i.ratioDeviationPct > 100
                ? badge(i.ratioDeviationPct.toFixed(0) + '%', 'red')
                : badge(i.ratioDeviationPct.toFixed(0) + '%', 'amber')}</td>
              <td>${i.wastedMemGB > 0
                ? i.wastedMemGB + ' GB mem'
                : i.wastedCPUCores + ' CPU cores'}</td>
              <td>${fmt$(i.wastedMonthlyCostUSD)}</td>
              <td>${sevBadge(i.severity)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (items.length > 0) {
    const table = $('#badratio-table');
    makeSortable(table);
    attachPagination(table);
  }
}

// --- KEDA / Autoscaler Issues Tab ---

async function renderKEDA(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/inefficiencies');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const items = data.keda || [];

  const issueTypeLabels = {
    'not-ready': 'Not Ready',
    'trigger-inactive': 'Trigger Inactive',
    'fallback': 'Fallback Mode',
    'paused': 'Paused',
    'hpa-metrics-failed': 'HPA Metrics Failed',
    'hpa-unable': 'HPA Unable to Scale',
    'hpa-no-metrics': 'No Metrics',
    'request-limit-mismatch': 'Request/Limit Mismatch',
  };

  const issueTypeColors = {
    'not-ready': 'red',
    'trigger-inactive': 'red',
    'fallback': 'amber',
    'paused': 'amber',
    'hpa-metrics-failed': 'red',
    'hpa-unable': 'red',
    'hpa-no-metrics': 'amber',
    'request-limit-mismatch': 'yellow',
  };

  targetEl.innerHTML = `
    ${items.length > 0 ? `
    <div class="card alert-border-amber mb-4">
      <div class="alert-card">
        <span class="alert-card-icon">&#9888;</span>
        <div>
          <strong>${items.length} autoscaler issue${items.length > 1 ? 's' : ''} detected</strong>
          <div class="alert-card-sub">
            These workloads have KEDA/HPA issues preventing proper scale-up or scale-down.
            Common causes: Prometheus unreachable, KEDA trigger misconfigured, CPU request too low relative to limit (HPA triggers scale-up based on request percentage, not absolute usage).
          </div>
        </div>
      </div>
    </div>` : ''}

    <div class="card">
      ${cardHeader('Autoscaler Issues (' + items.length + ')')}
      ${items.length === 0
        ? '<div class="empty-state-center text-small-muted">No autoscaler issues found</div>'
        : `<div class="table-wrap"><table id="keda-table">
          <thead><tr>
            <th>Workload</th>
            <th>Namespace</th>
            <th>ScaledObject/HPA</th>
            <th>Issue</th>
            <th>Replicas</th>
            <th>Min/Max</th>
            <th>CPU Req</th>
            <th>CPU Lim</th>
            <th>Req/Lim</th>
            <th>Severity</th>
          </tr></thead>
          <tbody>
            ${items.map(i => `<tr>
              <td><a href="#/workloads/${encodeURIComponent(i.namespace)}/Deployment/${encodeURIComponent(i.name)}">${escapeHtml(i.name)}</a></td>
              <td>${escapeHtml(i.namespace)}</td>
              <td class="cell-truncate" title="${escapeHtml(i.scaledObject)}">${escapeHtml(i.scaledObject)}</td>
              <td>${badge(issueTypeLabels[i.issueType] || i.issueType, issueTypeColors[i.issueType] || 'gray')}</td>
              <td>${i.currentReplicas}</td>
              <td>${i.minReplicas}/${i.maxReplicas}</td>
              <td>${escapeHtml(i.cpuRequest)}</td>
              <td>${escapeHtml(i.cpuLimit)}</td>
              <td>${i.requestLimitRatio > 0
                ? (i.requestLimitRatio < 0.3
                    ? badge((i.requestLimitRatio * 100).toFixed(0) + '%', 'red')
                    : (i.requestLimitRatio * 100).toFixed(0) + '%')
                : '-'}</td>
              <td>${sevBadge(i.severity)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>

    ${items.filter(i => i.problems && i.problems.length > 0).length > 0 ? `
    <div class="card">
      ${cardHeader('Problem Details')}
      <div style="padding:1rem">
        ${items.filter(i => i.problems && i.problems.length > 0).map(i => `
          <div style="margin-bottom:1rem;padding:0.75rem;border-radius:8px;background:var(--surface)">
            <div style="font-weight:600;margin-bottom:4px">${escapeHtml(i.namespace)}/${escapeHtml(i.name)}</div>
            <ul style="margin:0;padding-left:1.25rem;font-size:13px;color:var(--text-muted)">
              ${i.problems.map(p => `<li style="margin:2px 0">${escapeHtml(p)}</li>`).join('')}
            </ul>
          </div>
        `).join('')}
      </div>
    </div>` : ''}
  `;

  if (items.length > 0) {
    const table = $('#keda-table');
    makeSortable(table);
    attachPagination(table);
  }
}

// --- Bad PDBs Tab ---

async function renderPDB(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/inefficiencies');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const items = data.badPDBs || [];

  targetEl.innerHTML = `
    ${items.length > 0 ? `
    <div class="card alert-border-red mb-4">
      <div class="alert-card">
        <span class="alert-card-icon">&#9888;</span>
        <div>
          <strong>${items.length} problematic PDB${items.length > 1 ? 's' : ''} detected</strong>
          <div class="alert-card-sub">
            These PDBs are blocking node drain/scale-down or are orphaned.
            For fixing: use the <a href="#/scaledown" style="color:var(--primary)">Scale Down</a> page to delete blocking PDBs, or increase deployment replicas to allow disruptions.
          </div>
        </div>
      </div>
    </div>` : ''}

    <div class="card">
      ${cardHeader('Bad PDBs (' + items.length + ')')}
      ${items.length === 0
        ? '<div class="empty-state-center text-small-muted">No bad PDBs found</div>'
        : `<div class="table-wrap"><table id="pdb-table">
          <thead><tr>
            <th>Name</th>
            <th>Namespace</th>
            <th>Reason</th>
            <th>Expected Pods</th>
            <th>Healthy</th>
            <th>Disruptions</th>
            <th>Affected Nodes</th>
            <th>Age</th>
            <th>Severity</th>
          </tr></thead>
          <tbody>
            ${items.map(i => `<tr>
              <td>${escapeHtml(i.name)}</td>
              <td>${escapeHtml(i.namespace)}</td>
              <td style="max-width:300px;font-size:12px">${escapeHtml(i.reason)}</td>
              <td>${i.expectedPods}</td>
              <td>${i.currentHealthy}</td>
              <td>${i.disruptionsAllowed === 0 ? badge('0', 'red') : i.disruptionsAllowed}</td>
              <td>${i.affectedNodes}</td>
              <td>${ageBadge(i.ageDays)}</td>
              <td>${sevBadge(i.severity)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (items.length > 0) {
    const table = $('#pdb-table');
    makeSortable(table);
    attachPagination(table);
  }
}

function ageBadge(days) {
  if (days > 365) return badge(days + 'd', 'red');
  if (days > 90) return badge(days + 'd', 'amber');
  return badge(days + 'd', 'gray');
}

// --- Bad Pods Tab ---

async function renderBadPods(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/inefficiencies');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const items = data.badPods || [];

  targetEl.innerHTML = `
    ${items.length > 0 ? `
    <div class="card alert-border-amber mb-4">
      <div class="alert-card">
        <span class="alert-card-icon">&#9888;</span>
        <div>
          <strong>${items.length} pod${items.length > 1 ? 's' : ''} in bad state</strong>
          <div class="alert-card-sub">
            Pods in CrashLoopBackOff, OOMKilled, or other error states consume resources and can block node scale-down.
            Use the <a href="#/resources/actions" style="color:var(--primary)">Actions</a> page to clean up bad pods.
          </div>
        </div>
      </div>
    </div>` : ''}

    <div class="card">
      ${cardHeader('Bad State Pods (' + items.length + ')')}
      ${filterBar({ placeholder: 'Search pods...', filters: [
        { id: 'ns-filter', label: 'Namespace', options: [...new Set(items.map(p => p.namespace))].sort() },
        { id: 'status-filter', label: 'Status', options: [...new Set(items.map(p => p.status))].sort() },
      ]})}
      ${items.length === 0
        ? '<div class="empty-state-center text-small-muted">No bad state pods found</div>'
        : `<div class="table-wrap"><table id="badpods-table">
          <thead><tr>
            <th>Pod</th>
            <th>Namespace</th>
            <th>Node</th>
            <th>Status</th>
            <th>Ready</th>
            <th>Restarts</th>
            <th>Blocks Scale-Down</th>
            <th>Age</th>
            <th>Severity</th>
          </tr></thead>
          <tbody>
            ${items.map(i => `<tr data-ns="${escapeHtml(i.namespace)}" data-status="${escapeHtml(i.status)}">
              <td class="cell-truncate" title="${escapeHtml(i.name)}">${escapeHtml(i.name)}</td>
              <td>${escapeHtml(i.namespace)}</td>
              <td>${i.nodeName ? `<a href="#/nodes/${encodeURIComponent(i.nodeName)}">${escapeHtml(shortNode(i.nodeName))}</a>` : '-'}</td>
              <td>${statusBadge(i.status)}</td>
              <td>${escapeHtml(i.readiness)}</td>
              <td>${i.restarts > 10 ? badge(i.restarts, 'red') : i.restarts}</td>
              <td>${i.blocksScaleDown ? badge('Yes', 'red') : badge('No', 'green')}</td>
              <td>${escapeHtml(i.age)}</td>
              <td>${sevBadge(i.severity)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (items.length > 0) {
    const table = $('#badpods-table');
    makeSortable(table);
    attachPagination(table);
    attachFilterHandlers($('.filter-bar'), table);
  }
}

function statusBadge(status) {
  if (status === 'CrashLoopBackOff') return badge(status, 'red');
  if (status === 'OOMKilled') return badge(status, 'red');
  if (status === 'Error' || status === 'Failed') return badge(status, 'red');
  if (status.startsWith('Init:')) return badge(status, 'amber');
  if (status === 'ImagePullBackOff' || status === 'ErrImagePull') return badge(status, 'amber');
  if (status === 'Running') return badge(status, 'green');
  return badge(status, 'yellow');
}

// --- Fragmentation Tab ---

async function renderFragmentation(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/inefficiencies');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const items = data.fragmentation || [];

  targetEl.innerHTML = `
    ${items.length > 0 ? `
    <div class="card alert-border-amber mb-4">
      <div class="alert-card">
        <span class="alert-card-icon">&#9888;</span>
        <div>
          <strong>${items.length} node${items.length > 1 ? 's' : ''} with significant allocation-usage gap</strong>
          <div class="alert-card-sub">
            These nodes have high CPU/memory allocated (reserved by pods) but very low actual usage.
            This indicates over-provisioned workloads — pods requesting far more than they use.
            Use the <a href="#/resources/recommendations" style="color:var(--primary)">Recommendations</a> page to right-size workloads on these nodes.
          </div>
        </div>
      </div>
    </div>` : ''}

    <div class="card">
      ${cardHeader('Node Fragmentation / Over-Provisioned (' + items.length + ')')}
      ${items.length === 0
        ? '<div class="empty-state-center text-small-muted">No fragmented nodes found</div>'
        : `<div class="table-wrap"><table id="frag-table">
          <thead><tr>
            <th>Node</th>
            <th>Instance Type</th>
            <th>Pods</th>
            <th>CPU Alloc %</th>
            <th>CPU Usage %</th>
            <th>Gap</th>
            <th>Mem Alloc %</th>
            <th>Mem Usage %</th>
            <th>Cost/mo</th>
            <th>Severity</th>
          </tr></thead>
          <tbody>
            ${items.map(i => `<tr>
              <td><a href="#/nodes/${encodeURIComponent(i.nodeName)}">${escapeHtml(shortNode(i.nodeName))}</a></td>
              <td>${escapeHtml(i.instanceType || '-')}</td>
              <td>${i.podCount}</td>
              <td>${fmtPct(i.cpuAllocationPct / 100)}</td>
              <td>${fmtPct(i.cpuUsagePct / 100)}</td>
              <td>${badge((i.cpuAllocationPct - i.cpuUsagePct).toFixed(0) + '%', 'amber')}</td>
              <td>${fmtPct(i.memAllocationPct / 100)}</td>
              <td>${fmtPct(i.memUsagePct / 100)}</td>
              <td>${fmt$(i.monthlyCostUSD)}</td>
              <td>${sevBadge(i.severity)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (items.length > 0) {
    const table = $('#frag-table');
    makeSortable(table);
    attachPagination(table);
  }
}

// --- Network I/O Tab ---

function fmtGB(v) {
  if (v == null || v === 0) return '0';
  if (v >= 1000) return (v / 1000).toFixed(1) + ' TB';
  if (v >= 1) return v.toFixed(1) + ' GB';
  return (v * 1024).toFixed(0) + ' MB';
}

async function renderNetwork(targetEl) {
  targetEl.innerHTML = skeleton(5);
  let data;
  try {
    data = await api('/inefficiencies');
  } catch (err) {
    targetEl.innerHTML = errorMsg('Failed to load: ' + err.message);
    return;
  }

  const items = data.networkHogs || [];

  targetEl.innerHTML = `
    ${items.length > 0 ? `
    <div class="card alert-border-amber mb-4">
      <div class="alert-card">
        <span class="alert-card-icon">&#9888;</span>
        <div>
          <strong>${items.length} workload${items.length > 1 ? 's' : ''} with high network I/O</strong>
          <div class="alert-card-sub">
            These workloads have the highest cumulative network bytes (rx+tx) since pod start, as reported by kubelet stats.
            High network I/O can indicate chatty services, missing caching, or unexpected traffic patterns.
            Bytes shown are cumulative since pod start — longer-running pods naturally accumulate more.
          </div>
        </div>
      </div>
    </div>` : ''}

    <div class="card">
      ${cardHeader('Network I/O by Workload (' + items.length + ')')}
      ${filterBar({ placeholder: 'Search workloads...', filters: [
        { id: 'ns-filter', label: 'Namespace', options: [...new Set(items.map(p => p.namespace))].sort() },
      ]})}
      ${items.length === 0
        ? '<div class="empty-state-center text-small-muted">No network data available (requires kubelet stats)</div>'
        : `<div class="table-wrap"><table id="network-table">
          <thead><tr>
            <th>Workload</th>
            <th>Namespace</th>
            <th>Replicas</th>
            <th>Total Rx</th>
            <th>Total Tx</th>
            <th>Total I/O</th>
            <th>Per-Pod Rx</th>
            <th>Per-Pod Tx</th>
            <th>Severity</th>
          </tr></thead>
          <tbody>
            ${items.map(i => `<tr data-ns="${escapeHtml(i.namespace)}">
              <td><a href="#/workloads/${encodeURIComponent(i.namespace)}/${encodeURIComponent(i.kind)}/${encodeURIComponent(i.name)}">${escapeHtml(i.name)}</a></td>
              <td>${escapeHtml(i.namespace)}</td>
              <td>${i.replicas}</td>
              <td>${fmtGB(i.totalRxGB)}</td>
              <td>${fmtGB(i.totalTxGB)}</td>
              <td>${fmtGB(i.totalRxGB + i.totalTxGB)}</td>
              <td>${fmtGB(i.perPodRxGB)}</td>
              <td>${fmtGB(i.perPodTxGB)}</td>
              <td>${sevBadge(i.severity)}</td>
            </tr>`).join('')}
          </tbody>
        </table></div>`}
    </div>`;

  if (items.length > 0) {
    const table = $('#network-table');
    makeSortable(table);
    attachPagination(table);
    attachFilterHandlers($('.filter-bar'), table);
  }
}
