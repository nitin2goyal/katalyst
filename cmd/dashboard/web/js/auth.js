// Authentication module

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
    <div class="login-card">
      <div class="login-logo">
        <svg width="36" height="36" viewBox="0 0 28 28" fill="none">
          <rect width="28" height="28" rx="6" fill="#4361ee"/>
          <path d="M7 14l4 4 10-10" stroke="#fff" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span>Katalyst</span>
      </div>
      <form id="login-form">
        <input type="password" id="login-password" placeholder="Password" autofocus autocomplete="current-password" />
        <button type="submit">Sign In</button>
        <div id="login-error" class="login-error"></div>
      </form>
    </div>
  `;

  // Styles
  overlay.style.cssText = `
    position: fixed; inset: 0; z-index: 10000;
    display: flex; align-items: center; justify-content: center;
    background: var(--bg-primary, #0f1117);
  `;

  const style = document.createElement('style');
  style.textContent = `
    .login-card {
      background: var(--bg-secondary, #1a1d2e);
      border: 1px solid var(--border, #2a2d3e);
      border-radius: 12px;
      padding: 2.5rem;
      width: 340px;
      box-shadow: 0 8px 32px rgba(0,0,0,0.3);
    }
    .login-logo {
      display: flex; align-items: center; gap: 10px;
      font-size: 1.4rem; font-weight: 600;
      color: var(--text-primary, #e4e6f0);
      margin-bottom: 1.5rem;
      justify-content: center;
    }
    #login-form input {
      width: 100%; padding: 10px 12px;
      background: var(--bg-primary, #0f1117);
      border: 1px solid var(--border, #2a2d3e);
      border-radius: 8px; color: var(--text-primary, #e4e6f0);
      font-size: 0.95rem; outline: none;
      box-sizing: border-box;
    }
    #login-form input:focus {
      border-color: #4361ee;
    }
    #login-form button {
      width: 100%; padding: 10px; margin-top: 12px;
      background: #4361ee; color: #fff; border: none;
      border-radius: 8px; font-size: 0.95rem; font-weight: 500;
      cursor: pointer; transition: background 0.15s;
    }
    #login-form button:hover { background: #3451de; }
    .login-error {
      color: #ef4444; font-size: 0.85rem;
      margin-top: 8px; text-align: center; min-height: 1.2em;
    }
  `;
  overlay.appendChild(style);
  document.body.appendChild(overlay);

  document.getElementById('login-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const pw = document.getElementById('login-password').value;
    const errEl = document.getElementById('login-error');
    errEl.textContent = '';

    try {
      const res = await fetch('/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: pw }),
      });
      if (res.ok) {
        overlay.remove();
        onSuccess();
      } else {
        errEl.textContent = 'Invalid password';
        document.getElementById('login-password').value = '';
        document.getElementById('login-password').focus();
      }
    } catch (_) {
      errEl.textContent = 'Connection error';
    }
  });
}
