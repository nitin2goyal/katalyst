// Shared formatters and helpers

export const $ = (sel, ctx) => (ctx || document).querySelector(sel);
export const $$ = (sel, ctx) => [...(ctx || document).querySelectorAll(sel)];

export function toArray(data, ...keys) {
  if (Array.isArray(data)) return data;
  if (data == null) return [];
  for (const k of keys) {
    if (Array.isArray(data[k])) return data[k];
  }
  // Handle paginated response envelope {data: [...], total, page, ...}
  if (Array.isArray(data.data)) return data.data;
  return [];
}

export function fmt$(v) {
  if (v == null) return '$0';
  if (v >= 1000) return '$' + (v / 1000).toFixed(1) + 'k';
  return '$' + Number(v).toFixed(2);
}

export function fmtPct(v) { return v == null ? '0%' : Number(v).toFixed(1) + '%'; }
/** Format CPU: millicores (number) or string like "100m" → human-readable cores.
 *  1000m → "1", 1200m → "1.2", 250m → "0.25", "100m" → "0.1" */
export function fmtCPU(v) {
  if (v == null) return '0';
  let milli;
  if (typeof v === 'number') {
    milli = v;
  } else {
    const s = String(v).trim();
    if (s.endsWith('m')) milli = parseFloat(s);
    else { const n = parseFloat(s); return isNaN(n) ? v : (n >= 1 ? n.toString() : n + ''); }
  }
  if (isNaN(milli) || milli === 0) return '0';
  const cores = milli / 1000;
  if (cores >= 1) return cores % 1 === 0 ? cores.toString() : cores.toFixed(1);
  return cores.toFixed(2).replace(/0+$/, '').replace(/\.$/, '');
}

/** Format memory: bytes (number) or string like "1907Mi" → human-readable GB.
 *  1073741824 → "1 GB", "1907Mi" → "1.9 GB" */
export function fmtMem(v) {
  if (v == null) return '0';
  let bytes;
  if (typeof v === 'number') {
    bytes = v;
  } else {
    const s = String(v).trim();
    const m = s.match(/^([\d.]+)\s*(Ti|Gi|Mi|Ki|B)?$/i);
    if (m) {
      const n = parseFloat(m[1]);
      const unit = (m[2] || '').toLowerCase();
      if (unit === 'ti') bytes = n * 1099511627776;
      else if (unit === 'gi') bytes = n * 1073741824;
      else if (unit === 'mi') bytes = n * 1048576;
      else if (unit === 'ki') bytes = n * 1024;
      else bytes = n;
    } else return v;
  }
  if (isNaN(bytes) || bytes === 0) return '0';
  const gb = bytes / 1073741824;
  if (gb >= 1) return gb.toFixed(1).replace(/\.0$/, '') + ' GB';
  const mb = bytes / 1048576;
  if (mb >= 1) return Math.round(mb) + ' MB';
  return Math.round(bytes / 1024) + ' KB';
}

export function utilClass(pct) {
  if (pct < 50) return 'low';
  if (pct < 80) return 'mid';
  return 'high';
}

export function utilBar(pct) {
  const p = Math.min(100, Math.max(0, pct || 0));
  // Compact: colored percentage only, no progress bar (fits narrow table columns)
  return `<span class="util-pct ${utilClass(p)}">${fmtPct(p)}</span>`;
}

export function healthDot(status) {
  const s = (status || '').toLowerCase();
  const cls = s === 'healthy' || s === 'enabled' ? 'green'
    : s === 'degraded' ? 'yellow'
    : s === 'error' ? 'red'
    : s === 'disabled' ? 'gray'
    : 'gray';
  return `<span class="health-dot ${cls}"></span>`;
}

export function badge(text, cls) {
  return `<span class="badge badge-${cls}">${text || ''}</span>`;
}

export function errorMsg(msg) {
  return `<div class="error-msg">${msg}</div>`;
}

export function escapeHtml(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

export function timeAgo(dateStr) {
  const diff = Date.now() - new Date(dateStr).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return mins + 'm ago';
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return hrs + 'h ago';
  const days = Math.floor(hrs / 24);
  return days + 'd ago';
}
