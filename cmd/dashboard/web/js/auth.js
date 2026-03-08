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

  const style = document.createElement('style');
  style.textContent = `
    #login-overlay {
      position: fixed; inset: 0; z-index: 10000;
      display: flex; align-items: center; justify-content: center;
      background: #06060f;
      overflow: hidden;
      font-family: var(--font-sans, 'DM Sans', -apple-system, BlinkMacSystemFont, sans-serif);
    }

    /* Subtle dot grid background */
    .login-bg-grid {
      position: absolute; inset: 0;
      background-image: radial-gradient(rgba(255,255,255,0.03) 1px, transparent 1px);
      background-size: 24px 24px;
    }

    /* Ambient glow orbs */
    .login-glow {
      position: absolute;
      border-radius: 50%;
      filter: blur(80px);
      opacity: 0.35;
      pointer-events: none;
      animation: loginFloat 12s ease-in-out infinite alternate;
    }
    .login-glow-1 {
      width: 400px; height: 400px;
      background: #7c3aed;
      top: -10%; left: -5%;
      animation-delay: 0s;
    }
    .login-glow-2 {
      width: 350px; height: 350px;
      background: #00d4ff;
      bottom: -10%; right: -5%;
      animation-delay: -4s;
    }
    .login-glow-3 {
      width: 250px; height: 250px;
      background: #4361ee;
      top: 50%; left: 60%;
      transform: translate(-50%, -50%);
      animation-delay: -8s;
      opacity: 0.2;
    }

    @keyframes loginFloat {
      0%   { transform: translate(0, 0) scale(1); }
      100% { transform: translate(20px, -20px) scale(1.08); }
    }

    /* Card */
    .login-card {
      position: relative;
      background: rgba(255, 255, 255, 0.04);
      border: 1px solid rgba(255, 255, 255, 0.08);
      border-radius: 20px;
      padding: 3rem 2.5rem 2.5rem;
      width: 380px;
      max-width: calc(100vw - 2rem);
      backdrop-filter: blur(24px);
      -webkit-backdrop-filter: blur(24px);
      box-shadow:
        0 24px 48px rgba(0, 0, 0, 0.4),
        0 0 0 1px rgba(255, 255, 255, 0.04) inset,
        0 1px 0 rgba(255, 255, 255, 0.06) inset;
      text-align: center;
      animation: loginCardIn 0.5s cubic-bezier(0.16, 1, 0.3, 1) both;
    }

    @keyframes loginCardIn {
      from { opacity: 0; transform: translateY(16px) scale(0.97); }
      to   { opacity: 1; transform: translateY(0) scale(1); }
    }

    /* Logo icon */
    .login-icon-wrap {
      display: flex; justify-content: center;
      margin-bottom: 1.25rem;
    }
    .login-icon-ring {
      width: 64px; height: 64px;
      display: flex; align-items: center; justify-content: center;
      border-radius: 16px;
      background: rgba(255, 255, 255, 0.04);
      border: 1px solid rgba(255, 255, 255, 0.08);
      box-shadow: 0 0 32px rgba(124, 58, 237, 0.15), 0 0 32px rgba(0, 212, 255, 0.1);
    }

    /* Title */
    .login-title {
      font-size: 1.65rem;
      font-weight: 700;
      letter-spacing: -0.02em;
      background: linear-gradient(135deg, #e4e6f0 30%, #00d4ff 100%);
      -webkit-background-clip: text;
      -webkit-text-fill-color: transparent;
      background-clip: text;
      margin-bottom: 0.35rem;
    }
    .login-subtitle {
      color: #6b7280;
      font-size: 0.85rem;
      font-weight: 400;
      margin-bottom: 2rem;
      letter-spacing: 0.02em;
    }

    /* Form */
    #login-form {
      display: flex;
      flex-direction: column;
      gap: 0.75rem;
    }

    .login-input-wrap {
      position: relative;
    }
    .login-input-icon {
      position: absolute;
      left: 14px; top: 50%;
      transform: translateY(-50%);
      color: #4b5563;
      pointer-events: none;
      transition: color 0.2s;
    }
    .login-input-wrap:focus-within .login-input-icon {
      color: #00d4ff;
    }

    #login-form input {
      width: 100%;
      padding: 13px 14px 13px 42px;
      background: rgba(255, 255, 255, 0.04);
      border: 1px solid rgba(255, 255, 255, 0.08);
      border-radius: 12px;
      color: #e4e6f0;
      font-size: 0.95rem;
      font-family: inherit;
      outline: none;
      box-sizing: border-box;
      transition: border-color 0.2s, box-shadow 0.2s, background 0.2s;
    }
    #login-form input::placeholder {
      color: #4b5563;
    }
    #login-form input:focus {
      border-color: rgba(0, 212, 255, 0.4);
      background: rgba(0, 212, 255, 0.04);
      box-shadow: 0 0 0 3px rgba(0, 212, 255, 0.08);
    }

    /* Button */
    #login-form button {
      position: relative;
      width: 100%;
      padding: 13px 16px;
      margin-top: 4px;
      background: linear-gradient(135deg, #7c3aed, #4361ee);
      color: #fff;
      border: none;
      border-radius: 12px;
      font-size: 0.95rem;
      font-weight: 600;
      font-family: inherit;
      cursor: pointer;
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 8px;
      transition: transform 0.15s, box-shadow 0.15s, opacity 0.15s;
      box-shadow: 0 4px 16px rgba(124, 58, 237, 0.3);
      overflow: hidden;
    }
    #login-form button::before {
      content: '';
      position: absolute; inset: 0;
      background: linear-gradient(135deg, rgba(255,255,255,0.12), transparent 60%);
      border-radius: inherit;
    }
    #login-form button:hover {
      transform: translateY(-1px);
      box-shadow: 0 6px 24px rgba(124, 58, 237, 0.4);
    }
    #login-form button:active {
      transform: translateY(0);
      box-shadow: 0 2px 8px rgba(124, 58, 237, 0.3);
    }
    .login-btn-text, .login-btn-arrow {
      position: relative;
    }
    .login-btn-arrow {
      transition: transform 0.2s;
    }
    #login-form button:hover .login-btn-arrow {
      transform: translateX(3px);
    }

    /* Loading state */
    #login-form button.loading {
      pointer-events: none;
      opacity: 0.8;
    }
    #login-form button.loading .login-btn-text,
    #login-form button.loading .login-btn-arrow {
      opacity: 0;
    }
    #login-form button.loading::after {
      content: '';
      position: absolute;
      width: 18px; height: 18px;
      border: 2px solid rgba(255,255,255,0.3);
      border-top-color: #fff;
      border-radius: 50%;
      animation: loginSpin 0.6s linear infinite;
    }
    @keyframes loginSpin {
      to { transform: rotate(360deg); }
    }

    /* Error */
    .login-error {
      color: #ef4444;
      font-size: 0.82rem;
      min-height: 1.2em;
      transition: opacity 0.2s;
    }
    .login-error:empty {
      opacity: 0;
    }

    /* Shake animation on error */
    @keyframes loginShake {
      0%, 100% { transform: translateX(0); }
      20%, 60% { transform: translateX(-6px); }
      40%, 80% { transform: translateX(6px); }
    }
    .login-card.shake {
      animation: loginShake 0.4s ease-in-out;
    }

    /* Footer */
    .login-footer {
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 6px;
      margin-top: 1.75rem;
      padding-top: 1.25rem;
      border-top: 1px solid rgba(255, 255, 255, 0.05);
      color: #3b3f51;
      font-size: 0.75rem;
      letter-spacing: 0.03em;
    }
    .login-footer svg {
      opacity: 0.5;
    }
  `;
  overlay.appendChild(style);
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
        card.style.transition = 'opacity 0.3s, transform 0.3s';
        card.style.opacity = '0';
        card.style.transform = 'scale(0.96)';
        setTimeout(() => {
          overlay.remove();
          onSuccess();
        }, 250);
      } else {
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
