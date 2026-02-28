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
          <button class="mode-btn ${mode === 'enforce' ? 'mode-btn-active' : ''}" data-mode="enforce">
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
        <h2>Controllers</h2>
        <div class="controllers-grid" id="controllers-grid"></div>
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

    // Controllers grid
    const cg = $('#controllers-grid');
    const ctrlNames = Object.keys(controllers);
    cg.innerHTML = ctrlNames.map(name => {
      const enabled = controllers[name];
      return `<div class="controller-item">
        <div class="controller-name">${name}</div>
        <div>${enabled ? badge('Enabled', 'green') : badge('Disabled', 'gray')}</div>
      </div>`;
    }).join('');

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
            ${badge(t.capacityType || 'on-demand', t.capacityType === 'spot' ? 'amber' : 'blue')}
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
        toast(`Mode changed to ${newMode.charAt(0).toUpperCase() + newMode.slice(1)}`, 'success');
        renderSettings();
        // Update mode badge in sidebar
        const badgeEl = document.getElementById('mode-badge');
        if (badgeEl) {
          badgeEl.textContent = newMode.charAt(0).toUpperCase() + newMode.slice(1);
          badgeEl.className = 'mode-badge ' + newMode;
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
