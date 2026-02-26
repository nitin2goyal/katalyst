// Shared formatters and helpers

export const $ = (sel, ctx) => (ctx || document).querySelector(sel);
export const $$ = (sel, ctx) => [...(ctx || document).querySelectorAll(sel)];

export function toArray(data, ...keys) {
  if (Array.isArray(data)) return data;
  if (data == null) return [];
  for (const k of keys) {
    if (Array.isArray(data[k])) return data[k];
  }
  return [];
}

export function fmt$(v) {
  if (v == null) return '$0';
  if (v >= 1000) return '$' + (v / 1000).toFixed(1) + 'k';
  return '$' + Number(v).toFixed(2);
}

export function fmtPct(v) { return v == null ? '0%' : Number(v).toFixed(1) + '%'; }
export function fmtCPU(v) { return v || '0m'; }
export function fmtMem(v) { return v || '0Mi'; }

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
