import { api, apiPut, apiPost, apiDelete } from '../api.js';
import { $, toArray, timeAgo, errorMsg } from '../utils.js';
import { skeleton, toast, badge, cardHeader, emptyState, modal, confirmDialog } from '../components.js';

const container = () => $('#page-container');

function channelTypeBadge(type) {
  const colors = { slack: 'blue', teams: 'purple', email: 'green', webhook: 'gray' };
  return badge(type || 'webhook', colors[type] || 'blue');
}

function truncateURL(url, max = 50) {
  if (!url || url.length <= max) return url || '';
  return url.slice(0, max) + '...';
}

function renderChannelCard(ch) {
  const isStatic = ch.static;
  return `<div class="channel-card">
    <div class="channel-header">
      <span class="channel-type">${channelTypeBadge(ch.type)}</span>
      <span class="channel-status">${badge(ch.enabled ? 'active' : 'disabled', ch.enabled ? 'green' : 'gray')}</span>
    </div>
    <div class="channel-name">${ch.name || ch.type || ''}</div>
    <div class="channel-detail" title="${ch.target || ''}">${truncateURL(ch.target)}</div>
    ${!isStatic ? `<div class="channel-actions" style="margin-top:8px;display:flex;gap:8px">
      <button class="btn ${ch.enabled ? 'btn-gray' : 'btn-green'} btn-sm" data-toggle-channel="${ch.id}">
        ${ch.enabled ? 'Disable' : 'Enable'}
      </button>
      <button class="btn btn-red btn-sm" data-delete-channel="${ch.id}">Delete</button>
    </div>` : '<div style="margin-top:8px"><span style="color:var(--text-muted);font-size:11px">From config file</span></div>'}
  </div>`;
}

function showAddChannelModal() {
  const overlay = document.createElement('div');
  overlay.innerHTML = modal('Add Notification Channel',
    `<div style="display:flex;flex-direction:column;gap:12px">
      <div>
        <label style="display:block;font-size:12px;font-weight:600;margin-bottom:4px;color:var(--text-muted)">Type</label>
        <select class="filter-select" id="add-ch-type" style="width:100%">
          <option value="slack">Slack</option>
          <option value="teams">Teams</option>
        </select>
      </div>
      <div>
        <label style="display:block;font-size:12px;font-weight:600;margin-bottom:4px;color:var(--text-muted)">Name</label>
        <input type="text" class="filter-search" id="add-ch-name" placeholder="e.g. #ops-alerts" style="width:100%;box-sizing:border-box">
      </div>
      <div>
        <label style="display:block;font-size:12px;font-weight:600;margin-bottom:4px;color:var(--text-muted)">Webhook URL</label>
        <input type="text" class="filter-search" id="add-ch-url" placeholder="https://hooks.slack.com/..." style="width:100%;box-sizing:border-box">
      </div>
    </div>`,
    `<button class="btn btn-gray" data-action="cancel">Cancel</button>
     <button class="btn btn-blue" data-action="add">Add Channel</button>`
  );
  const el = overlay.firstElementChild;
  document.body.appendChild(el);

  el.querySelector('[data-action="cancel"]').onclick = () => el.remove();
  el.querySelector('[data-action="add"]').onclick = async () => {
    const type = el.querySelector('#add-ch-type').value;
    const name = el.querySelector('#add-ch-name').value.trim();
    const url = el.querySelector('#add-ch-url').value.trim();

    if (!name) { toast('Name is required', 'error'); return; }
    if (!url) { toast('Webhook URL is required', 'error'); return; }

    try {
      await apiPost('/notifications/channels', { type, name, url });
      toast(`${type.charAt(0).toUpperCase() + type.slice(1)} channel "${name}" added`, 'success');
      el.remove();
      renderSettings();
    } catch (err) {
      toast('Failed to add channel: ' + err.message, 'error');
    }
  };
}

export async function renderSettings() {
  container().innerHTML = skeleton(5);
  try {
    const [config, policiesData, notifData, auditData] = await Promise.all([
      api('/config'),
      api('/policies').catch(() => null),
      api('/notifications').catch(() => null),
      api('/audit').catch(() => []),
    ]);
    const mode = config.mode || 'recommend';
    const controllers = config.controllers || {};
    const dryRun = config.dryRun || {};

    // Policies data
    const templates = policiesData?.nodeTemplates || [];
    const policies = policiesData?.schedulingPolicies || [];

    // Notification channels
    const nd = notifData || {};
    const channels = toArray(nd, 'channels');

    // Audit log
    const auditEvents = toArray(auditData);

    container().innerHTML = `
      <div class="page-header"><h1>Settings</h1><p>Dashboard, cluster configuration, policies, and notifications</p></div>

      <div class="card">
        <h2>Cluster Information</h2>
        <div class="detail-list">
          <div class="detail-item"><span class="detail-label">Cloud Provider</span><span>${(config.cloudProvider || 'unknown').toUpperCase()}</span></div>
          <div class="detail-item"><span class="detail-label">Region</span><span>${config.region || 'unknown'}</span></div>
          <div class="detail-item"><span class="detail-label">Cluster Name</span><span>${config.clusterName || 'unknown'}</span></div>
        </div>
      </div>

      <div class="card">
        <h2>Operating Mode</h2>
        <p style="color:var(--text-muted);font-size:13px;margin-bottom:16px">Control how KOptimizer operates on your cluster</p>
        <div class="mode-toggle" id="mode-toggle">
          <button class="mode-btn ${mode === 'recommend' ? 'mode-btn-active' : ''}" data-mode="recommend">
            <span class="mode-btn-title">Recommend</span>
            <span class="mode-btn-desc">Suggest optimizations without applying</span>
          </button>
          <button class="mode-btn ${mode === 'active' ? 'mode-btn-active' : ''}" data-mode="active">
            <span class="mode-btn-title">Enforce</span>
            <span class="mode-btn-desc">Automatically apply optimizations</span>
          </button>
        </div>
      </div>

      <div class="card">
        <h2>Auto Pod Purger</h2>
        <p style="color:var(--text-muted);font-size:13px;margin-bottom:16px">Automatically delete pods stuck in error states (CrashLoopBackOff, OOMKilled, etc.) that have been in a bad state for at least 30 minutes</p>
        <div style="display:flex;align-items:center;gap:12px">
          <button class="btn ${controllers.podPurger ? 'btn-green' : 'btn-gray'} btn-sm" id="pod-purger-toggle">
            ${controllers.podPurger ? 'ON' : 'OFF'}
          </button>
          <span style="color:var(--text-muted);font-size:12px">${controllers.podPurger ? 'Purger is actively cleaning up bad pods' : 'Purger is disabled'}</span>
        </div>
      </div>

      <div class="card">
        <h2>GPU Node Reclaimer</h2>
        <p style="color:var(--text-muted);font-size:13px;margin-bottom:16px">Automatically evict non-GPU pods from GPU nodes when GPU workloads scale down, freeing expensive GPU hardware. Includes a 5-minute grace period and PDB-safe evictions.</p>
        <div style="display:flex;align-items:center;gap:12px">
          <button class="btn ${controllers.gpuReclaim ? 'btn-green' : 'btn-gray'} btn-sm" id="gpu-reclaim-toggle">
            ${controllers.gpuReclaim ? 'ON' : 'OFF'}
          </button>
          <span style="color:var(--text-muted);font-size:12px">${controllers.gpuReclaim ? 'Reclaimer is actively monitoring GPU nodes' : 'Reclaimer is disabled'}</span>
        </div>
      </div>

      <div class="card">
        <h2>Controllers</h2>
        <p style="color:var(--text-muted);font-size:13px;margin-bottom:16px">Enable or disable individual optimization controllers. Changes take effect immediately.</p>
        <div id="controllers-section"></div>
      </div>

      <div class="card">
        <h2>Dashboard Settings</h2>
        <div class="detail-list">
          <div class="detail-item">
            <span class="detail-label">Theme</span>
            <button class="btn btn-gray btn-sm" id="theme-toggle-settings">
              ${document.documentElement.getAttribute('data-theme') === 'light' ? 'Switch to Dark' : 'Switch to Light'}
            </button>
          </div>
        </div>
      </div>

      <div class="card">
        ${cardHeader('Policies & Templates')}
        ${templates.length ? `<div class="templates-grid" id="templates-grid"></div>` : ''}
        ${policies.length ? `
          <div style="margin-top:${templates.length ? '20' : '0'}px">
            <h3>Scheduling Policies</h3>
            <div class="table-wrap"><table id="policies-table">
              <thead><tr><th>Policy</th><th>Type</th><th>Target</th><th>Description</th><th>Status</th></tr></thead>
              <tbody>${policies.map(p => `<tr>
                <td style="font-weight:600">${p.name}</td>
                <td>${badge(p.type, 'blue')}</td>
                <td><code style="font-size:12px;background:var(--bg);padding:2px 6px;border-radius:4px">${p.target}</code></td>
                <td style="white-space:normal;color:var(--text-muted);font-size:12px">${p.description}</td>
                <td>${p.enabled ? badge('Active', 'green') : badge('Inactive', 'gray')}</td>
              </tr>`).join('')}</tbody>
            </table></div>
          </div>
        ` : ''}
        ${!templates.length && !policies.length ? emptyState('&#128203;', 'No policies or templates configured') : ''}
      </div>

      <div class="card">
        ${cardHeader('Notification Channels', '<button class="btn btn-blue btn-sm" id="add-channel-btn">Add Channel</button>')}
        <div class="channels-grid" id="channels-grid">
          ${channels.length ? channels.map(ch => renderChannelCard(ch)).join('') : emptyState('&#128276;', 'No notification channels configured')}
        </div>
      </div>

      <div class="card">
        ${cardHeader('Audit Log')}
        ${auditEvents.length ? `
          <div class="table-wrap"><table id="audit-table">
            <thead><tr><th>Time</th><th>Action</th><th>Target</th><th>Details</th><th>User</th></tr></thead>
            <tbody>${auditEvents.map(e => {
              const actionColor = e.action?.includes('approved') ? 'green' : e.action?.includes('dismissed') ? 'amber' : e.action?.includes('scaled') || e.action?.includes('rightsized') ? 'blue' : 'gray';
              return `<tr>
                <td style="white-space:nowrap">${timeAgo(e.timestamp)}</td>
                <td>${badge(e.action || '', actionColor)}</td>
                <td><code style="font-size:12px;background:var(--bg);padding:2px 6px;border-radius:4px">${e.target || ''}</code></td>
                <td style="white-space:normal;color:var(--text-muted);font-size:12px">${e.details || ''}</td>
                <td>${e.user || ''}</td>
              </tr>`;
            }).join('')}</tbody>
          </table></div>
        ` : emptyState('&#128220;', 'No audit events')}
      </div>

    `;

    // Controllers — categorized by risk level with toggles
    const controllerMeta = {
      costMonitor:    { label: 'Cost Monitor',      desc: 'Tracks cluster cost data and trends',                        category: 'monitoring' },
      nodegroupMgr:   { label: 'Node Group Manager', desc: 'Adjusts node group min counts based on utilization',         category: 'non-intrusive' },
      gpu:            { label: 'GPU Optimizer',      desc: 'Optimizes GPU node scheduling and utilization',              category: 'non-intrusive' },
      commitments:    { label: 'Commitments',        desc: 'Monitors reserved instance and CUD coverage',               category: 'monitoring' },
      nodeAutoscaler: { label: 'Node Autoscaler',    desc: 'Scales nodes up/down based on utilization thresholds',       category: 'mildly-intrusive', hasDryRun: true },
      evictor:        { label: 'Evictor',            desc: 'Drains underutilized nodes to consolidate workloads',        category: 'mildly-intrusive', hasDryRun: true },
      rebalancer:     { label: 'Rebalancer',         desc: 'Rebalances pods across nodes for better distribution',       category: 'mildly-intrusive', hasDryRun: true },
      rightsizer:     { label: 'Rightsizer',          desc: 'Adjusts workload CPU/memory requests to match actual usage', category: 'intrusive' },
      workloadScaler: { label: 'Workload Scaler',    desc: 'Scales workload replicas and configures HPAs',               category: 'intrusive' },
      aiGate:         { label: 'AI Safety Gate',      desc: 'AI review for high-impact changes (cost >$500 or >3 nodes)', category: 'safety' },
    };

    const categories = [
      { key: 'monitoring',        label: 'Monitoring',                      color: 'blue',  desc: 'Read-only data collection — no cluster changes' },
      { key: 'non-intrusive',     label: 'Non-Intrusive',                   color: 'green', desc: 'Adjusts infrastructure settings, does not move or restart pods' },
      { key: 'mildly-intrusive',  label: 'Mildly Intrusive',                color: 'amber', desc: 'May move pods between nodes but does not modify workload specs' },
      { key: 'intrusive',         label: 'Intrusive',                       color: 'red',   desc: 'Modifies workload specs (requests/limits/replicas) — triggers restarts' },
      { key: 'safety',            label: 'Safety',                          color: 'purple', desc: 'Safety controls for high-impact actions' },
    ];

    const cs = $('#controllers-section');
    cs.innerHTML = categories.map(cat => {
      const items = Object.entries(controllerMeta).filter(([, m]) => m.category === cat.key);
      if (!items.length) return '';
      return `<div style="margin-bottom:20px">
        <div style="display:flex;align-items:center;gap:8px;margin-bottom:10px">
          ${badge(cat.label, cat.color)}
          <span style="color:var(--text-muted);font-size:12px">${cat.desc}</span>
        </div>
        <div class="controllers-grid">${items.map(([name, meta]) => {
          const enabled = controllers[name] ?? false;
          const isDryRun = meta.hasDryRun && (dryRun[name] ?? true);
          return `<div class="controller-item" style="display:flex;justify-content:space-between;align-items:center">
            <div>
              <div class="controller-name" style="display:flex;align-items:center;gap:6px">
                ${meta.label}
                ${enabled && isDryRun ? badge('Dry Run', 'amber') : ''}
              </div>
              <div style="font-size:11px;color:var(--text-muted);margin-top:2px">${meta.desc}</div>
              ${enabled && isDryRun ? '<div style="font-size:11px;color:var(--amber);margin-top:2px">Logging only — actions are not being executed</div>' : ''}
            </div>
            <div style="display:flex;gap:6px;align-items:center">
              ${meta.hasDryRun && enabled ? `<button class="btn ${isDryRun ? 'btn-amber' : 'btn-blue'} btn-sm dryrun-toggle" data-ctrl="${name}" style="min-width:70px;font-size:11px" title="${isDryRun ? 'Currently in dry-run mode — click to execute actions for real' : 'Currently executing actions — click to switch to dry-run'}">
                ${isDryRun ? 'DRY RUN' : 'LIVE'}
              </button>` : ''}
              <button class="btn ${enabled ? 'btn-green' : 'btn-gray'} btn-sm ctrl-toggle" data-ctrl="${name}" style="min-width:50px">
                ${enabled ? 'ON' : 'OFF'}
              </button>
            </div>
          </div>`;
        }).join('')}</div>
      </div>`;
    }).join('');

    // Controller toggle handlers
    cs.addEventListener('click', async (e) => {
      const btn = e.target.closest('.ctrl-toggle');
      if (btn) {
        const name = btn.dataset.ctrl;
        const newState = !(controllers[name] ?? false);
        btn.disabled = true;
        btn.textContent = '...';
        try {
          await apiPut(`/config/controllers/${name}`, { enabled: newState });
          controllers[name] = newState;
          toast(`${controllerMeta[name]?.label || name} ${newState ? 'enabled' : 'disabled'}`, 'success');
          renderSettings(); // re-render to show/hide dry-run button
        } catch (err) {
          btn.textContent = controllers[name] ? 'ON' : 'OFF';
          toast('Failed to toggle controller: ' + err.message, 'error');
          btn.disabled = false;
        }
        return;
      }

      const dryBtn = e.target.closest('.dryrun-toggle');
      if (dryBtn) {
        const name = dryBtn.dataset.ctrl;
        const newDryRun = !(dryRun[name] ?? true);
        dryBtn.disabled = true;
        dryBtn.textContent = '...';
        try {
          await apiPut(`/config/controllers/${name}/dry-run`, { dryRun: newDryRun });
          dryRun[name] = newDryRun;
          toast(`${controllerMeta[name]?.label || name} ${newDryRun ? 'set to dry-run' : 'set to LIVE — actions will be executed'}`, newDryRun ? 'info' : 'success');
          renderSettings();
        } catch (err) {
          dryBtn.textContent = (dryRun[name] ?? true) ? 'DRY RUN' : 'LIVE';
          toast('Failed to toggle dry-run: ' + err.message, 'error');
          dryBtn.disabled = false;
        }
      }
    });

    // Node template cards
    if (templates.length) {
      const tg = $('#templates-grid');
      tg.innerHTML = templates.map(t => {
        const families = (t.instanceFamilies || []).join(', ');
        const excluded = (t.excludedTypes || []);
        const taints = (t.taints || []);
        const labels = t.labels || {};
        const zones = (t.zones || []).join(', ');
        const labelEntries = Object.entries(labels);
        return `<div class="template-card">
          <div class="template-header">
            <span class="template-name">${t.name}</span>
            ${badge(t.capacityType || 'on-demand', 'blue')}
          </div>
          <div class="template-desc">${t.description || ''}</div>
          <div class="template-details">
            <div class="template-row"><span class="template-label">Instance Families</span><span>${families}</span></div>
            <div class="template-row"><span class="template-label">Architecture</span><span>${t.architecture || 'amd64'}</span></div>
            <div class="template-row"><span class="template-label">Node Range</span><span>${t.minNodes} - ${t.maxNodes}</span></div>
            <div class="template-row"><span class="template-label">Zones</span><span>${zones}</span></div>
            ${excluded.length ? `<div class="template-row"><span class="template-label">Excluded Types</span><span class="red">${excluded.join(', ')}</span></div>` : ''}
            ${taints.length ? `<div class="template-row"><span class="template-label">Taints</span><span>${taints.map(t => `<code style="font-size:11px;background:var(--bg);padding:1px 4px;border-radius:3px">${t.key}=${t.value}:${t.effect}</code>`).join(' ')}</span></div>` : ''}
            ${labelEntries.length ? `<div class="template-row"><span class="template-label">Labels</span><span>${labelEntries.map(([k, v]) => `<code style="font-size:11px;background:var(--bg);padding:1px 4px;border-radius:3px">${k}=${v}</code>`).join(' ')}</span></div>` : ''}
          </div>
        </div>`;
      }).join('');
    }

    // Mode toggle handler
    $('#mode-toggle').addEventListener('click', async (e) => {
      const btn = e.target.closest('.mode-btn');
      if (!btn) return;
      const newMode = btn.dataset.mode;
      try {
        await apiPut('/config/mode', { mode: newMode });
        const displayLabel = newMode === 'active' ? 'Enforce' : newMode.charAt(0).toUpperCase() + newMode.slice(1);
        toast(`Mode changed to ${displayLabel}`, 'success');
        renderSettings();
        // Update mode badge in sidebar
        const badgeEl = document.getElementById('mode-badge');
        if (badgeEl) {
          badgeEl.textContent = displayLabel;
          badgeEl.className = 'mode-badge ' + (newMode === 'active' ? 'enforce' : newMode);
        }
      } catch (e) {
        toast('Failed to change mode: ' + e.message, 'error');
      }
    });

    // Pod purger toggle
    $('#pod-purger-toggle').addEventListener('click', async () => {
      const newState = !controllers.podPurger;
      try {
        await apiPut('/config/pod-purger', { enabled: newState });
        toast(`Auto Pod Purger ${newState ? 'enabled' : 'disabled'}`, 'success');
        renderSettings();
      } catch (e) {
        toast('Failed to toggle pod purger: ' + e.message, 'error');
      }
    });

    // GPU reclaim toggle
    $('#gpu-reclaim-toggle').addEventListener('click', async () => {
      const newState = !controllers.gpuReclaim;
      try {
        await apiPut('/config/controllers/gpuReclaim', { enabled: newState });
        toast(`GPU Node Reclaimer ${newState ? 'enabled' : 'disabled'}`, 'success');
        renderSettings();
      } catch (e) {
        toast('Failed to toggle GPU reclaimer: ' + e.message, 'error');
      }
    });

    // Theme toggle
    $('#theme-toggle-settings').addEventListener('click', () => {
      window.dispatchEvent(new CustomEvent('kopt-theme-toggle'));
      renderSettings();
    });

    // Add Channel button
    $('#add-channel-btn').addEventListener('click', () => showAddChannelModal());

    // Channel toggle and delete handlers (event delegation on the grid)
    $('#channels-grid').addEventListener('click', async (e) => {
      const toggleBtn = e.target.closest('[data-toggle-channel]');
      if (toggleBtn) {
        const idx = toggleBtn.dataset.toggleChannel;
        const ch = channels.find(c => String(c.id) === idx);
        if (!ch) return;
        try {
          await apiPut(`/notifications/channels/${idx}`, { enabled: !ch.enabled });
          toast(`Channel ${ch.enabled ? 'disabled' : 'enabled'}`, 'success');
          renderSettings();
        } catch (err) {
          toast('Failed to toggle channel: ' + err.message, 'error');
        }
        return;
      }

      const deleteBtn = e.target.closest('[data-delete-channel]');
      if (deleteBtn) {
        const idx = deleteBtn.dataset.deleteChannel;
        const ch = channels.find(c => String(c.id) === idx);
        confirmDialog(`Delete channel "${ch?.name || ''}"?`, async () => {
          try {
            await apiDelete(`/notifications/channels/${idx}`);
            toast('Channel deleted', 'success');
            renderSettings();
          } catch (err) {
            toast('Failed to delete channel: ' + err.message, 'error');
          }
        });
      }
    });

  } catch (e) {
    container().innerHTML = errorMsg('Failed to load settings: ' + e.message);
  }
}
