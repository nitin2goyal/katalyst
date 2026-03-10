// Authentication module
import { auditAction } from './api.js';

export async function checkAuth() {
  try {
    const res = await fetch('/auth/check');
    if (res.ok) {
      const data = await res.json();
      return data.authenticated === true;
    }
    return false;
  } catch (_) {
    return false;
  }
}

export function showLogin(onSuccess) {
  // Hide sidebar and content
  const sidebar = document.getElementById('sidebar');
  const content = document.getElementById('content');
  if (sidebar) sidebar.style.display = 'none';
  if (content) content.style.display = 'none';

  // Remove any existing login overlay
  const existing = document.getElementById('login-overlay');
  if (existing) existing.remove();

  const overlay = document.createElement('div');
  overlay.id = 'login-overlay';
  overlay.innerHTML = `
    <div class="login-bg-grid"></div>
    <div class="login-glow login-glow-1"></div>
    <div class="login-glow login-glow-2"></div>
    <div class="login-glow login-glow-3"></div>

    <div class="login-card">
      <div class="login-icon-wrap">
        <div class="login-icon-ring">
          <svg width="36" height="36" viewBox="0 0 32 32" fill="none">
            <rect width="32" height="32" rx="8" fill="url(#logo-grad)"/>
            <path d="M9 6v20" stroke="#fff" stroke-width="3.2" stroke-linecap="round"/>
            <path d="M12 16l8.5-9" stroke="#fff" stroke-width="3.2" stroke-linecap="round"/>
            <path d="M12 16l8.5 9" stroke="#fff" stroke-width="3.2" stroke-linecap="round"/>
            <circle cx="23.5" cy="5.5" r="2.2" fill="#00d4ff" opacity="0.9"/>
            <defs>
              <linearGradient id="logo-grad" x1="0" y1="0" x2="32" y2="32">
                <stop offset="0%" stop-color="#7c3aed"/>
                <stop offset="100%" stop-color="#00d4ff"/>
              </linearGradient>
            </defs>
          </svg>
        </div>
      </div>
      <h1 class="login-title">Katalyst</h1>
      <p class="login-subtitle">Kubernetes Cost Optimization</p>

      <form id="login-form">
        <div class="login-input-wrap">
          <svg class="login-input-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/>
            <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
          </svg>
          <input type="password" id="login-password" placeholder="Enter password" autofocus autocomplete="current-password" />
        </div>
        <button type="submit" id="login-btn">
          <span class="login-btn-text">Sign In</span>
          <svg class="login-btn-arrow" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
            <path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>
          </svg>
        </button>
        <div id="login-error" class="login-error"></div>
      </form>

      <div class="login-footer">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>
        </svg>
        <span>Secured session</span>
      </div>
    </div>
  `;

  document.body.appendChild(overlay);

  const form = document.getElementById('login-form');
  const btn = document.getElementById('login-btn');
  const card = overlay.querySelector('.login-card');

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const pw = document.getElementById('login-password').value;
    const errEl = document.getElementById('login-error');
    errEl.textContent = '';

    btn.classList.add('loading');

    try {
      const res = await fetch('/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: pw }),
      });
      if (res.ok) {
        auditAction('user.login', 'session', 'User logged in');
        card.style.transition = 'opacity 0.3s, transform 0.3s';
        card.style.opacity = '0';
        card.style.transform = 'scale(0.96)';
        setTimeout(() => {
          overlay.remove();
          onSuccess();
        }, 250);
      } else {
        auditAction('user.login-failed', 'session', 'Failed login attempt');
        btn.classList.remove('loading');
        errEl.textContent = 'Invalid password';
        card.classList.remove('shake');
        void card.offsetWidth; // reflow
        card.classList.add('shake');
        document.getElementById('login-password').value = '';
        document.getElementById('login-password').focus();
      }
    } catch (_) {
      btn.classList.remove('loading');
      errEl.textContent = 'Connection error';
    }
  });
}
