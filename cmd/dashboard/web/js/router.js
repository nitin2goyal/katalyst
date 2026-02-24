// Hash-based router with parameterized route support
import { $$ } from './utils.js';
import { destroyCharts } from './charts.js';

const routes = [];
let currentCleanup = null;

export function addRoute(pattern, handler) {
  // Convert pattern like '/nodes/{name}' to regex
  const paramNames = [];
  const regexStr = pattern.replace(/\{(\w+)\}/g, (_, name) => {
    paramNames.push(name);
    return '([^/]+)';
  });
  routes.push({ pattern, regex: new RegExp('^' + regexStr + '$'), paramNames, handler });
}

export function navigate(hash) {
  if (hash) location.hash = '#' + hash;
}

export function currentRoute() {
  return location.hash.replace('#', '') || '/overview';
}

export function getParams() {
  const path = currentRoute();
  for (const route of routes) {
    const match = path.match(route.regex);
    if (match) {
      const params = {};
      route.paramNames.forEach((name, i) => params[name] = decodeURIComponent(match[i + 1]));
      return params;
    }
  }
  return {};
}

function matchRoute(path) {
  for (const route of routes) {
    const match = path.match(route.regex);
    if (match) {
      const params = {};
      route.paramNames.forEach((name, i) => params[name] = decodeURIComponent(match[i + 1]));
      return { handler: route.handler, params };
    }
  }
  return null;
}

export function handleNavigation() {
  destroyCharts();
  if (currentCleanup) {
    currentCleanup();
    currentCleanup = null;
  }

  const path = currentRoute();
  const result = matchRoute(path);

  // Update active nav link - match exact or parent path
  $$('.nav-links a').forEach(a => {
    const href = a.getAttribute('href').replace('#', '');
    const isActive = path === href || (href !== '/overview' && path.startsWith(href + '/'));
    a.classList.toggle('active', isActive);
  });

  if (result) {
    const cleanup = result.handler(result.params);
    if (typeof cleanup === 'function') currentCleanup = cleanup;
  }
}

export function initRouter() {
  window.addEventListener('hashchange', handleNavigation);
  handleNavigation();
}
