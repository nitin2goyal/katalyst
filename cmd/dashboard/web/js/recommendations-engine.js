// Client-side recommendations engine — computes savings from live node/workload data
// when the backend API returns no recommendations (CRDs not installed).
import { api } from './api.js';

const HOURS_PER_MONTH = 730.5;
const MIN_SAVINGS = 5; // $5/mo threshold
const SYSTEM_NS = new Set(['kube-system', 'kube-public', 'kube-node-lease']);

let _cache = null;
let _cacheTs = 0;
const CACHE_TTL = 300_000; // 5 min

/**
 * Compute recommendations from live node + workload data.
 * Returns { recommendations: [...], opportunities: [...], totalSavings: number }
 */
export async function computeRecommendations() {
  if (_cache && Date.now() - _cacheTs < CACHE_TTL) return _cache;

  const [nodes, workloads, nodeGroups] = await Promise.all([
    api('/nodes').catch(() => []),
    api('/workloads').catch(() => []),
    api('/nodegroups').catch(() => []),
  ]);

  const nodeList = Array.isArray(nodes) ? nodes : (nodes?.nodes || []);
  const wlList = Array.isArray(workloads) ? workloads : (workloads?.workloads || []);
  const ngList = Array.isArray(nodeGroups) ? nodeGroups : (nodeGroups?.nodeGroups || []);

  const recs = [];
  const now = new Date().toISOString();

  // 1. Empty non-GPU nodes
  for (const n of nodeList) {
    if (n.isGPU || (n.podCount || 0) > 0) continue;
    const savings = (n.hourlyCostUSD || 0) * HOURS_PER_MONTH;
    if (savings < MIN_SAVINGS) continue;
    recs.push({
      id: hashId('consolidation', n.name),
      type: 'consolidation',
      target: n.name,
      description: `Node ${n.name} is empty (no pods). Remove to save $${Math.round(savings)}/mo.`,
      estimatedSavings: round2(savings),
      status: 'pending', priority: 'critical', createdAt: now, confidence: 95,
    });
  }

  // 2. Underutilized non-GPU nodes (both CPU & mem < 20%)
  for (const n of nodeList) {
    if (n.isGPU || (n.podCount || 0) === 0) continue;
    const cpuCap = n.cpuCapacity || 1;
    const memCap = n.memCapacity || 1;
    const cpuPct = (n.cpuUsed || 0) / cpuCap * 100;
    const memPct = (n.memUsed || 0) / memCap * 100;
    if (cpuPct >= 20 || memPct >= 20) continue;
    const savings = (n.hourlyCostUSD || 0) * HOURS_PER_MONTH;
    if (savings < MIN_SAVINGS) continue;
    const priority = (cpuPct < 10 && memPct < 10) ? 'high' : 'medium';
    recs.push({
      id: hashId('consolidation-underutil', n.name),
      type: 'consolidation',
      target: n.name,
      description: `Node ${n.name} is underutilized (CPU: ${cpuPct.toFixed(1)}%, Mem: ${memPct.toFixed(1)}%). Drain and remove to save $${Math.round(savings)}/mo.`,
      estimatedSavings: round2(savings),
      status: 'pending', priority, createdAt: now, confidence: 85,
    });
  }

  // 3. Spot adoption — on-demand non-GPU nodes grouped by node group
  const spotGroups = {};
  for (const n of nodeList) {
    if (n.isGPU || n.isSpot) continue;
    const gid = n.nodeGroup || n.nodeGroupId || 'ungrouped-' + (n.instanceType || 'unknown');
    if (!spotGroups[gid]) spotGroups[gid] = { name: n.nodeGroup || gid, count: 0, hourly: 0 };
    spotGroups[gid].count++;
    spotGroups[gid].hourly += (n.hourlyCostUSD || 0);
  }
  for (const [gid, sg] of Object.entries(spotGroups)) {
    const savings = sg.hourly * 0.65 * HOURS_PER_MONTH;
    if (savings < MIN_SAVINGS) continue;
    recs.push({
      id: hashId('spot', gid),
      type: 'spot',
      target: sg.name,
      description: `Convert ${sg.count} on-demand nodes (${sg.name}) to spot instances to save $${Math.round(savings)}/mo (est. 65% discount).`,
      estimatedSavings: round2(savings),
      status: 'pending', priority: 'medium', createdAt: now, confidence: 75,
    });
  }

  // 4. Node group rightsizing — groups with both CPU & mem util < 25%
  for (const ng of ngList) {
    const count = ng.currentCount || 0;
    if (count < 2) continue;
    const cpuUtil = ng.cpuUtilPct ?? ng.cpuUtilization ?? null;
    const memUtil = ng.memUtilPct ?? ng.memUtilization ?? null;
    if (cpuUtil == null || memUtil == null) continue;
    if (cpuUtil >= 25 || memUtil >= 25) continue;
    const maxUtil = Math.max(cpuUtil, memUtil) || 1;
    const target50 = Math.max(1, Math.ceil(count * maxUtil / 50));
    const removable = count - target50;
    if (removable <= 0) continue;
    const monthlyCost = ng.monthlyCostUSD || 0;
    const avgHourly = monthlyCost / HOURS_PER_MONTH / count;
    const savings = removable * avgHourly * HOURS_PER_MONTH;
    if (savings < MIN_SAVINGS) continue;
    const name = ng.name || ng.id || 'unknown';
    recs.push({
      id: hashId('consolidation-ng', ng.id || name),
      type: 'consolidation',
      target: name,
      description: `Node group ${name} has ${count} nodes at ${cpuUtil.toFixed(0)}% CPU, ${memUtil.toFixed(0)}% mem. Reduce by ${removable} nodes to save $${Math.round(savings)}/mo.`,
      estimatedSavings: round2(savings),
      status: 'pending', priority: 'medium', createdAt: now, confidence: 80,
    });
  }

  // Sort by savings desc
  recs.sort((a, b) => b.estimatedSavings - a.estimatedSavings);

  // Dedup by target (take max) to avoid counting consolidation + spot for same group
  const bestByTarget = {};
  for (const r of recs) {
    if (!bestByTarget[r.target] || r.estimatedSavings > bestByTarget[r.target]) {
      bestByTarget[r.target] = r.estimatedSavings;
    }
  }
  const totalSavings = Object.values(bestByTarget).reduce((s, v) => s + v, 0);

  const opportunities = recs.map(r => ({
    type: r.type,
    name: r.target,
    description: r.description,
    estimatedSavings: r.estimatedSavings,
  }));

  _cache = { recommendations: recs, opportunities, totalSavings };
  _cacheTs = Date.now();
  return _cache;
}

function round2(v) { return Math.round(v * 100) / 100; }

// Simple deterministic hash for stable IDs
function hashId(type, target) {
  let h = 0;
  const s = type + ':' + target;
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) - h + s.charCodeAt(i)) | 0;
  }
  return 'computed-' + Math.abs(h).toString(16).padStart(8, '0');
}
