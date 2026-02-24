import puppeteer from 'puppeteer';

const BASE = 'http://localhost:3000';
const TIMEOUT = 10000;
const issues = [];
const passed = [];

function log(msg) { console.log(`  ${msg}`); }
function pass(msg) { passed.push(msg); log(`PASS: ${msg}`); }
function fail(msg) { issues.push(msg); log(`FAIL: ${msg}`); }

async function waitForContent(page) {
  // Wait for skeleton to disappear (page loaded)
  await page.waitForFunction(
    () => !document.querySelector('.skeleton-wrap'),
    { timeout: TIMEOUT }
  ).catch(() => {});
  await new Promise(r => setTimeout(r, 500));
}

async function collectConsoleErrors(page) {
  const errors = [];
  page.on('console', msg => {
    if (msg.type() === 'error') errors.push(msg.text());
  });
  page.on('pageerror', err => errors.push(err.message));
  return errors;
}

async function checkApiErrors(page) {
  const errors = [];
  page.on('response', res => {
    if (res.url().includes('/api/') && res.status() >= 400) {
      errors.push(`${res.status()} ${res.url()}`);
    }
  });
  return errors;
}

// ── Test: Overview Page ──
async function testOverview(page) {
  console.log('\n=== Overview Page ===');
  const consoleErrors = await collectConsoleErrors(page);
  const apiErrors = await checkApiErrors(page);

  await page.goto(`${BASE}/#/overview`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);

  // Check page header
  const header = await page.$eval('.page-header h1', el => el.textContent).catch(() => null);
  header ? pass('Overview header renders') : fail('Overview header missing');

  // Check cluster score renders
  const score = await page.$('.score-circle').catch(() => null);
  score ? pass('Cluster score renders') : fail('Cluster score missing');

  // Check CPU/Memory utilization gauges
  const cpuGauge = await page.$('#cpu-gauge').catch(() => null);
  const memGauge = await page.$('#mem-gauge').catch(() => null);
  cpuGauge ? pass('CPU utilization gauge renders') : fail('CPU utilization gauge missing');
  memGauge ? pass('Memory utilization gauge renders') : fail('Memory utilization gauge missing');

  // Check allocation gauges
  const cpuAllocGauge = await page.$('#cpu-alloc-gauge').catch(() => null);
  const memAllocGauge = await page.$('#mem-alloc-gauge').catch(() => null);
  cpuAllocGauge ? pass('CPU allocation gauge renders') : fail('CPU allocation gauge missing');
  memAllocGauge ? pass('Memory allocation gauge renders') : fail('Memory allocation gauge missing');

  // Check node groups table
  const ngTable = await page.$('table').catch(() => null);
  ngTable ? pass('Node groups table renders') : fail('Node groups table missing');

  // Check for JS errors
  if (consoleErrors.length) fail(`JS errors: ${consoleErrors.join('; ')}`);
  else pass('No JS console errors');

  if (apiErrors.length) fail(`API errors: ${apiErrors.join('; ')}`);
  else pass('No API errors');
}

// ── Test: Cost Page ──
async function testCost(page) {
  console.log('\n=== Cost Page ===');
  await page.goto(`${BASE}/#/cost`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);

  const header = await page.$eval('.page-header h1', el => el.textContent).catch(() => null);
  header ? pass('Cost header renders') : fail('Cost header missing');

  // Check cost summary cards
  const cards = await page.$$('.card').catch(() => []);
  cards.length > 0 ? pass(`Cost page has ${cards.length} cards`) : fail('Cost page has no cards');

  // Check tabs
  const tabs = await page.$$('.tab').catch(() => []);
  tabs.length > 0 ? pass(`Cost tabs render (${tabs.length})`) : fail('Cost tabs missing');

  // Test tab navigation
  if (tabs.length > 1) {
    await tabs[1].click();
    await new Promise(r => setTimeout(r, 500));
    const activeTab = await page.$eval('.tab-active', el => el.textContent).catch(() => '');
    activeTab ? pass(`Tab navigation works (active: ${activeTab})`) : fail('Tab navigation broken');
  }
}

// ── Test: Resources Page ──
async function testResources(page) {
  console.log('\n=== Resources Page ===');
  await page.goto(`${BASE}/#/resources`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);

  const header = await page.$eval('.page-header h1', el => el.textContent).catch(() => null);
  header ? pass('Resources header renders') : fail('Resources header missing');

  // Check for node list or table
  const table = await page.$('table').catch(() => null);
  table ? pass('Resources table renders') : fail('Resources table missing');

  // Check tabs
  const tabs = await page.$$('.tab').catch(() => []);
  tabs.length > 0 ? pass(`Resources tabs render (${tabs.length})`) : fail('Resources tabs missing');

  // Test workloads tab
  await page.goto(`${BASE}/#/resources/workloads`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);
  const wlTable = await page.$('table').catch(() => null);
  wlTable ? pass('Workloads table renders') : fail('Workloads table missing');

  // Test recommendations tab
  await page.goto(`${BASE}/#/resources/recommendations`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);
  const recTable = await page.$('table').catch(() => null);
  recTable ? pass('Recommendations table renders') : fail('Recommendations table missing');
}

// ── Test: Infrastructure Page ──
async function testInfrastructure(page) {
  console.log('\n=== Infrastructure Page ===');
  await page.goto(`${BASE}/#/infrastructure`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);

  const header = await page.$eval('.page-header h1', el => el.textContent).catch(() => null);
  header ? pass('Infrastructure header renders') : fail('Infrastructure header missing');

  const cards = await page.$$('.card').catch(() => []);
  cards.length > 0 ? pass(`Infrastructure has ${cards.length} cards`) : fail('Infrastructure has no cards');

  // Test GPU tab
  await page.goto(`${BASE}/#/infrastructure/gpu`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);
  const gpuContent = await page.$('.card').catch(() => null);
  gpuContent ? pass('GPU tab renders') : fail('GPU tab broken');

  // Test commitments tab
  await page.goto(`${BASE}/#/infrastructure/commitments`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);
  const commitContent = await page.$('.card').catch(() => null);
  commitContent ? pass('Commitments tab renders') : fail('Commitments tab broken');
}

// ── Test: Settings Page ──
async function testSettings(page) {
  console.log('\n=== Settings Page ===');
  await page.goto(`${BASE}/#/settings`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);

  const header = await page.$eval('.page-header h1', el => el.textContent).catch(() => null);
  header === 'Settings' ? pass('Settings header renders') : fail(`Settings header: "${header}"`);

  // Check cluster info
  const clusterInfo = await page.$eval('.detail-list', el => el.textContent).catch(() => '');
  clusterInfo.includes('AWS') ? pass('Cluster info shows provider') : fail('Cluster info missing provider');

  // Check mode toggle
  const modeToggle = await page.$('#mode-toggle').catch(() => null);
  modeToggle ? pass('Mode toggle renders') : fail('Mode toggle missing');

  // Check active mode button
  const activeMode = await page.$eval('.mode-btn-active .mode-btn-title', el => el.textContent).catch(() => '');
  activeMode ? pass(`Active mode: ${activeMode}`) : fail('No active mode button');

  // Test mode change: click Enforce
  const enforceBtn = await page.$('.mode-btn[data-mode="enforce"]');
  if (enforceBtn) {
    await enforceBtn.click();
    await new Promise(r => setTimeout(r, 1500));

    // Check if toast appeared
    const toast = await page.$('.toast').catch(() => null);
    toast ? pass('Mode change shows toast') : fail('Mode change toast missing');

    // Wait for re-render
    await waitForContent(page);

    // Check mode persisted - the active button should now be Enforce
    const newActive = await page.$eval('.mode-btn-active .mode-btn-title', el => el.textContent).catch(() => '');
    newActive === 'Enforce' ? pass('Mode change persists to Enforce') : fail(`Mode change did not persist (active: "${newActive}")`);

    // Check sidebar badge updated
    const badgeText = await page.$eval('#mode-badge', el => el.textContent).catch(() => '');
    badgeText.toLowerCase().includes('enforce') ? pass('Sidebar badge updated to Enforce') : fail(`Sidebar badge: "${badgeText}"`);

    // Switch back to Recommend
    const recBtn = await page.$('.mode-btn[data-mode="recommend"]');
    if (recBtn) {
      await recBtn.click();
      await new Promise(r => setTimeout(r, 1500));
      await waitForContent(page);
    }
  } else {
    fail('Enforce button not found');
  }

  // Check controllers grid
  const controllers = await page.$$('.controller-item').catch(() => []);
  controllers.length > 0 ? pass(`Controllers grid has ${controllers.length} items`) : fail('Controllers grid empty');

  // Check dashboard settings
  const refreshSelect = await page.$('#refresh-interval').catch(() => null);
  refreshSelect ? pass('Refresh interval selector renders') : fail('Refresh interval selector missing');

  // Check theme toggle button
  const themeBtn = await page.$('#theme-toggle-settings').catch(() => null);
  themeBtn ? pass('Theme toggle button renders') : fail('Theme toggle button missing');

  // Test theme toggle
  if (themeBtn) {
    const beforeTheme = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    await themeBtn.click();
    await new Promise(r => setTimeout(r, 1000));
    const afterTheme = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    beforeTheme !== afterTheme ? pass('Theme toggle works') : fail('Theme toggle did not change theme');
    // Toggle back
    const newThemeBtn = await page.$('#theme-toggle-settings');
    if (newThemeBtn) await newThemeBtn.click();
    await new Promise(r => setTimeout(r, 500));
  }

  // Check policies section
  const policiesTable = await page.$('#policies-table').catch(() => null);
  policiesTable ? pass('Policies table renders') : fail('Policies table missing');

  // Check templates
  const templates = await page.$$('.template-card').catch(() => []);
  templates.length > 0 ? pass(`Templates render (${templates.length})`) : fail('Templates missing');

  // Check notification channels
  const channelCards = await page.$$('.channel-card').catch(() => []);
  channelCards.length > 0 ? pass(`Notification channels render (${channelCards.length})`) : fail('Notification channels missing');

  // Check audit log section
  const auditTable = await page.$('#audit-table').catch(() => null);
  auditTable ? pass('Audit log table renders') : fail('Audit log table missing');

  if (auditTable) {
    const auditRows = await page.$$('#audit-table tbody tr').catch(() => []);
    auditRows.length > 0 ? pass(`Audit log has ${auditRows.length} events`) : fail('Audit log has no events');
  }
}

// ── Test: Sidebar Navigation ──
async function testSidebar(page) {
  console.log('\n=== Sidebar Navigation ===');
  await page.goto(`${BASE}/#/overview`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);

  // Check mode badge
  const modeBadge = await page.$eval('#mode-badge', el => el.textContent).catch(() => '');
  modeBadge && modeBadge !== 'loading...' ? pass(`Mode badge shows: ${modeBadge}`) : fail(`Mode badge: "${modeBadge}"`);

  // Check mode badge is capitalized
  const isCapitalized = modeBadge.charAt(0) === modeBadge.charAt(0).toUpperCase();
  isCapitalized ? pass('Mode badge is capitalized') : fail('Mode badge not capitalized');

  // Check theme toggle button
  const themeToggle = await page.$('#theme-toggle').catch(() => null);
  themeToggle ? pass('Sidebar theme toggle renders') : fail('Sidebar theme toggle missing');

  // Test sidebar theme toggle
  if (themeToggle) {
    const beforeTheme = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    await themeToggle.click();
    await new Promise(r => setTimeout(r, 500));
    const afterTheme = await page.evaluate(() => document.documentElement.getAttribute('data-theme'));
    beforeTheme !== afterTheme ? pass('Sidebar theme toggle works') : fail('Sidebar theme toggle did not change theme');
    // Toggle back
    await themeToggle.click();
    await new Promise(r => setTimeout(r, 300));
  }

  // Test sidebar mode badge click
  const badge = await page.$('#mode-badge');
  if (badge) {
    const beforeMode = await page.$eval('#mode-badge', el => el.textContent.trim().toLowerCase());
    await badge.click();
    await new Promise(r => setTimeout(r, 1500));
    const afterMode = await page.$eval('#mode-badge', el => el.textContent.trim().toLowerCase());
    beforeMode !== afterMode ? pass(`Sidebar mode toggle works (${beforeMode} -> ${afterMode})`) : fail('Sidebar mode badge click did not change mode');
    // Toggle back
    await badge.click();
    await new Promise(r => setTimeout(r, 1000));
  }

  // Test nav links highlight
  const navLinks = [
    { hash: '#/overview', label: 'Overview' },
    { hash: '#/cost', label: 'Cost' },
    { hash: '#/resources', label: 'Resources' },
    { hash: '#/infrastructure', label: 'Infrastructure' },
    { hash: '#/settings', label: 'Settings' },
  ];
  for (const { hash, label } of navLinks) {
    await page.goto(`${BASE}/${hash}`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
    await new Promise(r => setTimeout(r, 300));
    const activeHref = await page.$eval('.nav-links a.active', el => el.getAttribute('href')).catch(() => '');
    activeHref === hash ? pass(`Nav "${label}" highlights correctly`) : fail(`Nav "${label}" highlight wrong (expected ${hash}, got ${activeHref})`);
  }
}

// ── Test: Node Detail Page ──
async function testNodeDetail(page) {
  console.log('\n=== Node Detail Page ===');
  await page.goto(`${BASE}/#/resources`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);

  // Links are onclick="location.hash='#/nodes/...'" on clickable-row tr elements
  // Find a row whose onclick contains '/nodes/' (not /nodegroups/)
  const allRows = await page.$$('tr.clickable-row');
  let nodeRow = null;
  for (const row of allRows) {
    const onclick = await page.evaluate(el => el.getAttribute('onclick') || '', row);
    if (onclick.includes('/nodes/') && !onclick.includes('/nodegroups/')) {
      nodeRow = row;
      break;
    }
  }
  if (nodeRow) {
    await nodeRow.click();
    await new Promise(r => setTimeout(r, 500));
    await waitForContent(page);

    const hash = await page.evaluate(() => location.hash);
    hash.includes('/nodes/') ? pass(`Node detail navigated: ${hash}`) : fail(`Node row click navigated to: ${hash}`);

    const breadcrumbs = await page.$('.breadcrumbs').catch(() => null);
    breadcrumbs ? pass('Node detail has breadcrumbs') : fail('Node detail breadcrumbs missing');

    const cards = await page.$$('.card').catch(() => []);
    cards.length > 0 ? pass(`Node detail has ${cards.length} cards`) : fail('Node detail has no cards');
  } else {
    fail('No clickable node rows (with /nodes/ in onclick) on resources page');
  }
}

// ── Test: Workload Detail Page ──
async function testWorkloadDetail(page) {
  console.log('\n=== Workload Detail Page ===');
  await page.goto(`${BASE}/#/resources/workloads`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);

  const wlRow = await page.$('tr.clickable-row').catch(() => null);
  if (wlRow) {
    await wlRow.click();
    await new Promise(r => setTimeout(r, 500));
    await waitForContent(page);

    const hash = await page.evaluate(() => location.hash);
    hash.includes('/workloads/') ? pass(`Workload detail navigated: ${hash}`) : fail(`Workload row click navigated to: ${hash}`);

    const breadcrumbs = await page.$('.breadcrumbs').catch(() => null);
    breadcrumbs ? pass('Workload detail has breadcrumbs') : fail('Workload detail breadcrumbs missing');

    const cards = await page.$$('.card').catch(() => []);
    cards.length > 0 ? pass(`Workload detail has ${cards.length} cards`) : fail('Workload detail has no cards');
  } else {
    fail('No clickable workload rows on resources page');
  }
}

// ── Test: Node Group Detail Page ──
async function testNodeGroupDetail(page) {
  console.log('\n=== Node Group Detail Page ===');
  await page.goto(`${BASE}/#/overview`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
  await waitForContent(page);

  // Node group rows are in a table at the bottom of overview
  const ngRows = await page.$$('tr.clickable-row');
  // Find a row whose onclick contains 'nodegroups'
  let found = false;
  for (const row of ngRows) {
    const onclick = await page.evaluate(el => el.getAttribute('onclick') || '', row);
    if (onclick.includes('nodegroups')) {
      await row.click();
      await new Promise(r => setTimeout(r, 500));
      await waitForContent(page);

      const hash = await page.evaluate(() => location.hash);
      hash.includes('/nodegroups/') ? pass(`Node group detail navigated: ${hash}`) : fail(`Node group row click navigated to: ${hash}`);

      const breadcrumbs = await page.$('.breadcrumbs').catch(() => null);
      breadcrumbs ? pass('Node group detail has breadcrumbs') : fail('Node group detail breadcrumbs missing');

      const cards = await page.$$('.card').catch(() => []);
      cards.length > 0 ? pass(`Node group detail has ${cards.length} cards`) : fail('Node group detail has no cards');

      found = true;
      break;
    }
  }
  if (!found) fail('No node group clickable rows found on overview page');
}

// ── Test: Backward-compat Redirects ──
async function testRedirects(page) {
  console.log('\n=== Backward-compat Redirects ===');
  const redirectTests = [
    { from: '#/savings', to: '#/cost/savings' },
    { from: '#/nodes', to: '#/resources' },
    { from: '#/workloads', to: '#/resources/workloads' },
    { from: '#/gpu', to: '#/infrastructure/gpu' },
    { from: '#/audit', to: '#/settings' },
  ];

  for (const { from, to } of redirectTests) {
    await page.goto(`${BASE}/${from}`, { waitUntil: 'networkidle2', timeout: TIMEOUT });
    await new Promise(r => setTimeout(r, 800));
    const currentHash = await page.evaluate(() => location.hash);
    currentHash === to ? pass(`Redirect ${from} -> ${to}`) : fail(`Redirect ${from}: expected ${to}, got ${currentHash}`);
  }
}

// ── Main ──
(async () => {
  const browser = await puppeteer.launch({
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox'],
  });

  const page = await browser.newPage();
  await page.setViewport({ width: 1440, height: 900 });

  // Collect global errors
  const globalErrors = [];
  page.on('pageerror', err => globalErrors.push(err.message));

  try {
    await testOverview(page);
    await testCost(page);
    await testResources(page);
    await testInfrastructure(page);
    await testSettings(page);
    await testSidebar(page);
    await testNodeDetail(page);
    await testWorkloadDetail(page);
    await testNodeGroupDetail(page);
    await testRedirects(page);
  } catch (err) {
    fail(`Test runner error: ${err.message}`);
  }

  if (globalErrors.length) {
    console.log('\n=== Global JS Errors ===');
    globalErrors.forEach(e => fail(`JS Error: ${e}`));
  }

  console.log('\n' + '='.repeat(50));
  console.log(`RESULTS: ${passed.length} passed, ${issues.length} failed`);
  if (issues.length) {
    console.log('\nFAILURES:');
    issues.forEach((issue, i) => console.log(`  ${i + 1}. ${issue}`));
  }
  console.log('='.repeat(50));

  await browser.close();
  process.exit(issues.length > 0 ? 1 : 0);
})();
