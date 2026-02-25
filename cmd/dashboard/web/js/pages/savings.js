import { api } from '../api.js';
import { $, toArray, fmt$, fmtPct, errorMsg } from '../utils.js';
import { makeChart } from '../charts.js';
import { skeleton, makeSortable, filterBar, attachFilterHandlers, cardHeader, badge, exportCSV } from '../components.js';
import { computeRecommendations } from '../recommendations-engine.js';

export async function renderSavings(targetEl) {
  const container = () => targetEl || $('#page-container');
  container().innerHTML = skeleton(5);
  try {
    const [savings, costSummary] = await Promise.all([
      api('/cost/savings'), api('/cost/summary')
    ]);
    let list = toArray(savings, 'opportunities', 'savings');
    // Fallback: compute from live node data when API returns empty
    if (!list.length) {
      const computed = await computeRecommendations();
      list = computed.opportunities;
    }
    const cs = costSummary || {};
    
    // Calculate category totals
    const categories = {};
    list.forEach(s => {
      const type = s.type || 'other';
      categories[type] = (categories[type] || 0) + (s.estimatedSavings || s.savings || 0);
    });
    const totalSavings = Object.values(categories).reduce((a, b) => a + b, 0);
    const projected = cs.projectedMonthlyCostUSD || cs.totalMonthlyCostUSD || 0;
    const savingsPct = projected > 0 ? (totalSavings / projected * 100) : 0;

    container().innerHTML = `
      ${!targetEl ? '<div class="page-header"><h1>Available Savings</h1><p>Optimization scenarios and savings breakdown</p></div>' : ''}
      <div class="kpi-grid">
        <div class="kpi-card"><div class="label">Total Identified</div><div class="value green">${fmt$(totalSavings)}</div><div class="sub">${fmtPct(savingsPct)} of projected spend</div></div>
        <div class="kpi-card"><div class="label">Projected Monthly</div><div class="value">${fmt$(projected)}</div><div class="sub">current trajectory</div></div>
        <div class="kpi-card"><div class="label">After Optimization</div><div class="value blue">${fmt$(Math.max(0, projected - totalSavings))}</div><div class="sub">optimized monthly cost</div></div>
        <div class="kpi-card"><div class="label">Opportunities</div><div class="value">${list.length}</div><div class="sub">actionable items</div></div>
      </div>
      <div class="grid-2">
        <div class="card">
          <h2>Savings by Category</h2>
          <div class="chart-container"><canvas id="savings-cat-chart"></canvas></div>
        </div>
        <div class="card">
          <h2>Scenario Simulator</h2>
          <div class="scenario-sim">
            <div class="sim-control">
              <label>Spot Instance Adoption</label>
              <div class="sim-row"><input type="range" id="sim-spot" min="0" max="100" value="30" class="sim-slider"><span id="sim-spot-val" class="sim-val">30%</span></div>
            </div>
            <div class="sim-control">
              <label>Rightsizing Aggressiveness</label>
              <div class="sim-row"><input type="range" id="sim-rs" min="0" max="100" value="50" class="sim-slider"><span id="sim-rs-val" class="sim-val">50%</span></div>
            </div>
            <div class="sim-control">
              <label>Commitment Coverage</label>
              <div class="sim-row"><input type="range" id="sim-commit" min="0" max="100" value="40" class="sim-slider"><span id="sim-commit-val" class="sim-val">40%</span></div>
            </div>
            <div class="sim-result">
              <div class="sim-result-label">Estimated Savings</div>
              <div class="sim-result-value" id="sim-total">${fmt$(totalSavings)}</div>
              <div class="sim-result-sub" id="sim-pct">${fmtPct(savingsPct)} reduction</div>
            </div>
          </div>
        </div>
      </div>
      <div class="card">
        ${cardHeader('Savings Opportunities', '<button class="btn btn-gray btn-sm" onclick="window.__exportSavingsDetailCSV()">Export CSV</button>')}
        ${filterBar({ placeholder: 'Search opportunities...', filters: [
          { key: '0', label: 'Type', options: [...new Set(list.map(s => s.type).filter(Boolean))] }
        ] })}
        <div class="table-wrap"><table id="savings-detail-table">
          <thead><tr><th>Type</th><th>Target</th><th>Description</th><th>Est. Monthly Savings</th><th>Impact</th></tr></thead>
          <tbody id="savings-detail-body"></tbody>
        </table></div>
      </div>`;

    // Category chart
    const catLabels = Object.keys(categories);
    const catColors = { rightsizing: '#4361ee', spot: '#10b981', commitment: '#f59e0b', consolidation: '#8b5cf6', other: '#94a3b8' };
    if (catLabels.length) {
      makeChart('savings-cat-chart', {
        type: 'bar',
        data: {
          labels: catLabels.map(l => l.charAt(0).toUpperCase() + l.slice(1)),
          datasets: [{ label: 'Monthly Savings ($)', data: catLabels.map(k => categories[k]), backgroundColor: catLabels.map(k => catColors[k] || '#94a3b8'), borderRadius: 6 }]
        },
        options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: { y: { beginAtZero: true } } }
      });
    } else {
      const el = document.getElementById('savings-cat-chart');
      if (el) el.parentElement.innerHTML = '<div style="display:flex;align-items:center;justify-content:center;height:200px;color:var(--text-muted);font-size:13px;">No savings categories identified yet</div>';
    }

    // Table
    const impactLevel = (v) => v >= 500 ? 'high' : v >= 100 ? 'medium' : 'low';
    const impactBadge = (v) => {
      const l = impactLevel(v);
      const cls = l === 'high' ? 'red' : l === 'medium' ? 'amber' : 'gray';
      return badge(l, cls);
    };
    $('#savings-detail-body').innerHTML = list.length ? list.map(s => {
      const amt = s.estimatedSavings || s.savings || 0;
      return `<tr>
        <td>${badge(s.type || 'optimization', 'blue')}</td>
        <td><strong>${s.name || s.target || ''}</strong></td>
        <td>${s.description || ''}</td>
        <td class="value green">${fmt$(amt)}</td>
        <td>${impactBadge(amt)}</td>
      </tr>`;
    }).join('') : '<tr><td colspan="5" style="color:var(--text-muted)">No savings identified</td></tr>';
    makeSortable($('#savings-detail-table'));

    // Filter
    const fb = container().querySelector('.filter-bar');
    if (fb) attachFilterHandlers(fb, $('#savings-detail-table'));

    // Scenario simulator interactivity
    const spotSlider = $('#sim-spot');
    const rsSlider = $('#sim-rs');
    const commitSlider = $('#sim-commit');
    function updateSim() {
      const spotPct = parseInt(spotSlider.value) / 100;
      const rsPct = parseInt(rsSlider.value) / 100;
      const commitPct = parseInt(commitSlider.value) / 100;
      $('#sim-spot-val').textContent = spotSlider.value + '%';
      $('#sim-rs-val').textContent = rsSlider.value + '%';
      $('#sim-commit-val').textContent = commitSlider.value + '%';
      const spotSave = (categories['spot'] || 0) * spotPct / 0.3;
      const rsSave = (categories['rightsizing'] || 0) * rsPct / 0.5;
      const commitSave = (categories['commitment'] || 0) * commitPct / 0.4;
      const otherSave = (categories['consolidation'] || 0) + (categories['other'] || 0);
      const simTotal = spotSave + rsSave + commitSave + otherSave;
      const simPct = projected > 0 ? (simTotal / projected * 100) : 0;
      $('#sim-total').textContent = fmt$(simTotal);
      $('#sim-pct').textContent = fmtPct(simPct) + ' reduction';
    }
    spotSlider?.addEventListener('input', updateSim);
    rsSlider?.addEventListener('input', updateSim);
    commitSlider?.addEventListener('input', updateSim);

    // CSV
    window.__exportSavingsDetailCSV = () => {
      exportCSV(['Type', 'Target', 'Description', 'Est. Monthly Savings'],
        list.map(s => [s.type, s.name || s.target, s.description, s.estimatedSavings || s.savings]),
        'koptimizer-savings-report.csv');
    };
  } catch (e) {
    container().innerHTML = errorMsg('Failed to load savings: ' + e.message);
  }
}
