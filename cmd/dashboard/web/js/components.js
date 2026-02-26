// Reusable UI components
import { $, $$, badge } from './utils.js';
export { badge };

export function skeleton(rows = 3) {
  return '<div class="skeleton-wrap">' +
    Array.from({ length: rows }, (_, i) =>
      `<div class="skeleton-line" style="width:${85 - i * 12}%"></div>`
    ).join('') + '</div>';
}

let toastTimer;
export function toast(message, type = 'info') {
  let container = $('#toast-container');
  if (!container) {
    container = document.createElement('div');
    container.id = 'toast-container';
    document.body.appendChild(container);
  }
  const el = document.createElement('div');
  el.className = `toast toast-${type}`;
  el.textContent = message;
  container.appendChild(el);
  // trigger animation
  requestAnimationFrame(() => el.classList.add('toast-show'));
  setTimeout(() => {
    el.classList.remove('toast-show');
    setTimeout(() => el.remove(), 300);
  }, 3000);
}

export function breadcrumbs(items) {
  return `<nav class="breadcrumbs">${items.map((item, i) => {
    if (i === items.length - 1) return `<span class="bc-current">${item.label}</span>`;
    return `<a href="${item.href}" class="bc-link">${item.label}</a><span class="bc-sep">/</span>`;
  }).join('')}</nav>`;
}

export function filterBar(options = {}) {
  const { placeholder = 'Search...', filters = [], onSearch, onFilter } = options;
  const id = 'fb-' + Math.random().toString(36).slice(2, 8);
  const filterHtml = filters.map(f =>
    `<select class="filter-select" data-filter="${f.key}">
      <option value="">${f.label}</option>
      ${f.options.map(o => `<option value="${o}">${o}</option>`).join('')}
    </select>`
  ).join('');

  return `<div class="filter-bar" id="${id}">
    <input type="text" class="filter-search" placeholder="${placeholder}">
    ${filterHtml}
  </div>`;
}

export function attachFilterHandlers(containerEl, tableEl) {
  if (!containerEl || !tableEl) return;
  const input = containerEl.querySelector('.filter-search');
  const selects = containerEl.querySelectorAll('.filter-select');

  function applyFilters() {
    const searchText = (input?.value || '').toLowerCase();
    const filterValues = {};
    selects.forEach(s => {
      if (s.value) filterValues[s.dataset.filter] = s.value.toLowerCase();
    });

    const rows = $$('tbody tr', tableEl);
    rows.forEach(row => {
      const text = row.textContent.toLowerCase();
      const matchSearch = !searchText || text.includes(searchText);
      let matchFilters = true;
      for (const [key, val] of Object.entries(filterValues)) {
        const colIdx = parseInt(key, 10);
        if (!isNaN(colIdx)) {
          const cellText = (row.children[colIdx]?.textContent || '').toLowerCase();
          matchFilters = matchFilters && cellText.includes(val);
        } else {
          matchFilters = matchFilters && text.includes(val);
        }
      }
      row.style.display = matchSearch && matchFilters ? '' : 'none';
    });
  }

  input?.addEventListener('input', applyFilters);
  selects.forEach(s => s.addEventListener('change', applyFilters));
}

export function modal(title, body, actions = '') {
  return `<div class="modal-overlay" onclick="if(event.target===this)this.remove()">
    <div class="modal">
      <div class="modal-header"><h3>${title}</h3><button class="modal-close" onclick="this.closest('.modal-overlay').remove()">&times;</button></div>
      <div class="modal-body">${body}</div>
      ${actions ? `<div class="modal-actions">${actions}</div>` : ''}
    </div>
  </div>`;
}

export function confirmDialog(message, onConfirm) {
  const overlay = document.createElement('div');
  overlay.innerHTML = modal('Confirm', `<p>${message}</p>`,
    `<button class="btn btn-gray" data-action="cancel">Cancel</button>
     <button class="btn btn-blue" data-action="confirm">Confirm</button>`);
  const el = overlay.firstElementChild;
  document.body.appendChild(el);
  el.querySelector('[data-action="cancel"]').onclick = () => el.remove();
  el.querySelector('[data-action="confirm"]').onclick = () => { el.remove(); onConfirm(); };
}

export function tabs(items, active) {
  return `<div class="tabs">${items.map(item =>
    `<button class="tab ${item.id === active ? 'tab-active' : ''}" data-tab="${item.id}">${item.label}</button>`
  ).join('')}</div>`;
}

export function attachTabHandlers(containerEl, onChange) {
  if (!containerEl) return;
  containerEl.querySelectorAll('.tab').forEach(btn => {
    btn.addEventListener('click', () => {
      containerEl.querySelectorAll('.tab').forEach(b => b.classList.remove('tab-active'));
      btn.classList.add('tab-active');
      onChange(btn.dataset.tab);
    });
  });
}

export function dateRangePicker(active = '30d') {
  const ranges = ['7d', '30d', '90d'];
  return `<div class="date-range-picker">${ranges.map(r =>
    `<button class="drp-btn ${r === active ? 'drp-active' : ''}" data-range="${r}">${r}</button>`
  ).join('')}</div>`;
}

export function exportCSV(headers, rows, filename) {
  const csvContent = [
    headers.join(','),
    ...rows.map(row => row.map(cell => {
      const str = String(cell ?? '').replace(/"/g, '""');
      return str.includes(',') || str.includes('"') || str.includes('\n') ? `"${str}"` : str;
    }).join(','))
  ].join('\n');
  const blob = new Blob([csvContent], { type: 'text/csv;charset=utf-8;' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

// In-memory sort state persisted across data refreshes (not cleared by store.clear())
const sortState = new Map();

function applySortToTable(tableEl, headers, colIdx, asc) {
  const tbody = $('tbody', tableEl);
  const rows = $$('tr', tbody).filter(r => r.style.display !== 'none');
  headers.forEach(h => { h.classList.remove('sorted-asc', 'sorted-desc'); });
  headers[colIdx].classList.add(asc ? 'sorted-asc' : 'sorted-desc');
  rows.sort((a, b) => {
    const at = a.children[colIdx]?.textContent.trim() || '';
    const bt = b.children[colIdx]?.textContent.trim() || '';
    const an = parseFloat(at.replace(/[$,%k]/g, ''));
    const bn = parseFloat(bt.replace(/[$,%k]/g, ''));
    if (!isNaN(an) && !isNaN(bn)) return asc ? an - bn : bn - an;
    return asc ? at.localeCompare(bt) : bt.localeCompare(at);
  });
  rows.forEach(r => tbody.appendChild(r));
}

export function makeSortable(tableEl) {
  if (!tableEl) return;
  const tableId = tableEl.id;
  const headers = $$('thead th', tableEl);
  headers.forEach((th, idx) => {
    th.addEventListener('click', () => {
      const asc = !th.classList.contains('sorted-asc');
      applySortToTable(tableEl, headers, idx, asc);
      if (tableId) sortState.set(tableId, { colIdx: idx, asc });
    });
  });
  // Restore saved sort state after re-render
  if (tableId && sortState.has(tableId)) {
    const { colIdx, asc } = sortState.get(tableId);
    if (colIdx < headers.length) {
      applySortToTable(tableEl, headers, colIdx, asc);
    }
  }
}

export function emptyState(icon, message) {
  return `<div class="empty-state">
    <div class="empty-state-icon">${icon}</div>
    <div class="empty-state-msg">${message}</div>
  </div>`;
}

export function cardHeader(title, rightHtml = '') {
  return `<div class="card-header"><h2>${title}</h2><div class="card-header-actions">${rightHtml}</div></div>`;
}

export function exportButton(onClick) {
  return `<button class="btn btn-gray btn-sm" onclick="${onClick}">Export CSV</button>`;
}
