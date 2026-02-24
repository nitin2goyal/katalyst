# KOptimizer Usage Guide

## Table of Contents

- [1. Overview](#1-overview)
- [2. Quick Start](#2-quick-start)
- [3. Architecture](#3-architecture)
- [4. Configuration Reference](#4-configuration-reference)
- [5. Operating Modes](#5-operating-modes)
- [6. Helm Deployment](#6-helm-deployment)
- [7. REST API Reference](#7-rest-api-reference)
- [8. MCP Server](#8-mcp-server)
- [9. Cloud Provider Setup](#9-cloud-provider-setup)
- [10. Monitoring](#10-monitoring)
- [11. Safety Guarantees](#11-safety-guarantees)
- [12. Troubleshooting](#12-troubleshooting)

---

## 1. Overview

KOptimizer is a Kubernetes cost optimization engine that automatically reduces cloud spend while preserving cluster stability. It is inspired by tools like CAST AI but takes a fundamentally different approach to node management.

### The Problem with Existing Optimizers

Traditional Kubernetes cost optimizers pick arbitrary instance families when scaling -- they might swap your `m5` nodes for `c5` or `r5` nodes, create new node groups with different instance types, and break your Reserved Instance or Savings Plan commitments in the process. This leads to degraded performance and wasted money on unused reservations.

### The KOptimizer Approach: Family-Locked Optimization with AI Safety Gate

KOptimizer enforces two core safety principles:

1. **Family-Lock Guard** -- The system never changes instance families and never creates new node groups. It scales existing node groups up and down, adjusts parameters like min/max counts, and rightsizes within the same instance family (e.g., `m5.large` to `m5.xlarge` is allowed, but `m5` to `c5` is blocked). This preserves your Reserved Instance and Savings Plan commitments.

2. **AI Safety Gate** -- Before executing any large or risky change, KOptimizer calls Claude to validate the decision. If the AI rejects the change, it falls back to a human-approved recommendation. If the AI API is unreachable, the change also falls back to recommendation mode.

### The 10 Features

| # | Feature | Description |
|---|---------|-------------|
| 1 | **Cost Monitoring** | Real-time cost visibility by namespace, workload, and label |
| 2 | **Node Autoscaling** | Scale existing node groups up/down, rightsize within family |
| 3 | **Node Group Lifecycle** | Adjust min/max counts, detect and recommend deletion of empty groups |
| 4 | **Workload Rightsizing** | Auto-apply optimal CPU/memory requests based on actual usage |
| 5 | **Workload Autoscaler** | Unified HPA+VPA that resolves scaling conflicts |
| 6 | **Evictor / Bin-Packing** | Consolidate pods onto fewer nodes, drain underutilized nodes |
| 7 | **Rebalancer** | Redistribute workloads for optimal packing on a schedule |
| 8 | **GPU Optimization** | Detect idle GPUs, run CPU workloads on idle GPU nodes |
| 9 | **Commitment Reporting** | Track RI/Savings Plan/CUD utilization and expiry |
| 10 | **AI Safety Gate** | Claude validates risky changes before execution |

### Multi-Cloud Support

KOptimizer supports AWS (EKS), GCP (GKE), and Azure (AKS).

---

## 2. Quick Start

### Prerequisites

- **Go 1.22+** (for building from source)
- **Kubernetes cluster** (EKS, GKE, or AKS)
- **kubectl** configured with cluster access
- **Helm 3** (for deployment)
- **Docker** (optional, for building container images)

### Build from Source

```bash
# Clone the repository
git clone https://github.com/koptimizer/koptimizer.git
cd koptimizer

# Build the main optimizer binary
make build
# Output: bin/koptimizer

# Build the MCP server binary
make build-mcp
# Output: bin/koptimizer-mcp

# Run all tests
make test

# Run linter
make lint

# Generate CRDs and deepcopy methods
make generate
```

### Docker Image

```bash
# Build the Docker image (includes both koptimizer and koptimizer-mcp binaries)
make docker-build
# Output: koptimizer:latest

# Tag and push to your registry
docker tag koptimizer:latest your-registry.com/koptimizer:latest
docker push your-registry.com/koptimizer:latest
```

The Docker image is based on `gcr.io/distroless/static:nonroot` and runs as non-root user (UID 65532). It exposes port 8080 (REST API) and port 9090 (Prometheus metrics).

### Helm Install (Minimal)

```bash
# Install with defaults (recommend mode, AWS, no AI Gate API key)
helm install koptimizer deploy/helm/koptimizer \
  --namespace koptimizer --create-namespace \
  --set config.cloudProvider=aws \
  --set config.region=us-east-1

# Install with AI Safety Gate enabled
helm install koptimizer deploy/helm/koptimizer \
  --namespace koptimizer --create-namespace \
  --set config.cloudProvider=aws \
  --set config.region=us-east-1 \
  --set config.mode=active \
  --set aiGateApiKey=sk-ant-xxxxx

# Install using an existing Kubernetes secret for the API key
helm install koptimizer deploy/helm/koptimizer \
  --namespace koptimizer --create-namespace \
  --set config.cloudProvider=aws \
  --set config.region=us-east-1 \
  --set aiGateApiKeySecretRef.name=my-anthropic-secret \
  --set aiGateApiKeySecretRef.key=ANTHROPIC_API_KEY
```

### Run Locally (Development)

```bash
# Ensure KUBECONFIG is set or ~/.kube/config exists
export KUBECONFIG=~/.kube/config

# Run with default config
./bin/koptimizer --config config.yaml

# Run with custom addresses
./bin/koptimizer \
  --config config.yaml \
  --metrics-bind-address :9090 \
  --health-probe-bind-address :8081
```

---

## 3. Architecture

```
+----------------------------------------------------------------------+
|                            KOptimizer                                 |
|                                                                       |
|  +-------------+ +-------------+ +----------+ +-------------------+  |
|  | Cost        | | Node        | | Evictor  | | GPU               |  |
|  | Monitor     | | Autoscaler  | |          | | Optimizer         |  |
|  +------+------+ +------+------+ +----+-----+ +--------+----------+  |
|         |               |              |                |             |
|  +------+------+ +------+------+ +----+-----+ +--------+----------+  |
|  | Commitments | | Node Group  | | Rebalancer | Workload          |  |
|  | Reporter    | | Manager     | |          | | Scaler+Rightsizer |  |
|  +------+------+ +------+------+ +----+-----+ +--------+----------+  |
|         |               |              |                |             |
|         |       +-------v--------+     |                |             |
|         |       | Family-Lock    |     |                |             |
|         |       | Guard          |<----+                |             |
|         |       | (NEVER change  |                      |             |
|         |       |  families or   |                      |             |
|         |       |  create new    |                      |             |
|         |       |  node groups)  |                      |             |
|         |       +-------+--------+                      |             |
|         |               |                               |             |
|         +-------+-------+-------+-----------+-----------+             |
|                 |               |           |                         |
|         +-------v--------+     |   +-------v--------+                |
|         | AI Safety Gate |     |   | Recommendation |                |
|         | (Claude)       |     |   | Engine (CRDs)  |                |
|         +-------+--------+     |   +-------+--------+                |
|                 |               |           |                         |
|         +-------v---------------v-----------v--------+                |
|         |           Cluster State Cache              |                |
|         +-------+------------------+--------+--------+                |
|                 |                  |        |                         |
|          +------v------+  +-------v-----+ +v-----------+             |
|          | REST API    |  | Prometheus  | | CRD Writer |             |
|          | (:8080)     |  | Metrics     | | (audit)    |             |
|          +-------------+  | (:9090)     | +------------+             |
|                           +-------------+                            |
+----------------------------------------------------------------------+
        ^                           ^
        | Cloud APIs                | Kubernetes API
        | (pricing, ASG/MIG/VMSS,   | (nodes, pods, metrics,
        |  commitments)             |  deployments, HPA, PDB,
        |                           |  cordon, drain, evict)
   +----+--------+            +----+--------+
   | AWS / GCP / |            | kube-apiserver
   | Azure APIs  |            |              |
   +--------------+            +--------------+
```

### How Controllers Work Together

1. **Cluster State Cache** refreshes every `reconcileInterval` (default: 60s), pulling node, pod, and metrics data from the Kubernetes API and cloud provider APIs.

2. **Cost Monitor** and **Commitment Reporter** are read-only controllers that compute cost attribution and track commitment utilization.

3. **Node Autoscaler** watches for unschedulable pods (triggers scale-up) and underutilized nodes (triggers scale-down). All node operations pass through the **Family-Lock Guard**, which verifies instance family consistency.

4. **Node Group Manager** watches for underutilized node groups (adjusts min counts) and empty node groups (recommends deletion).

5. **Rightsizer** analyzes workload CPU/memory usage over a lookback window and patches resource requests. **Workload Scaler** coordinates HPA and VPA to prevent oscillation.

6. **Evictor** consolidates pods from underutilized nodes. **Rebalancer** periodically redistributes workloads for optimal bin-packing.

7. **GPU Optimizer** detects idle GPU nodes and manages taints to allow CPU workloads as fallback.

8. For any risky change (large scale events, setting min to 0, cost impact above threshold), the **AI Safety Gate** validates the decision before execution.

### The Family-Lock Guard and AI Safety Gate Flow

```
Controller proposes action
         |
         v
  Family-Lock Guard
  "Is this within the same instance family?"
  "Is this modifying an existing node group (not creating)?"
         |
    +----+----+
    |         |
  BLOCKED   ALLOWED
  (logged)    |
              v
      Is this a risky change?
      (>30% scale, min->0, >$500 impact, >3 node eviction)
         |
    +----+----+
    |         |
    NO       YES
    |         |
    v         v
  Execute   AI Safety Gate (Claude)
  directly    |
         +----+----+
         |         |
      APPROVED  REJECTED (or API error/timeout)
         |         |
         v         v
      Execute   Create Recommendation CRD
      directly  (human must approve)
```

---

## 4. Configuration Reference

KOptimizer loads configuration from a YAML file (default path: `/etc/koptimizer/config.yaml`). Any values not specified in the file fall back to built-in defaults. The config file path is set via the `--config` flag.

### Full YAML Reference

```yaml
# Operating mode: "monitor" | "recommend" | "active"
# Default: "recommend"
mode: "recommend"

# Cloud provider: "aws" | "gcp" | "azure"
# No default -- must be set for cloud-specific features
cloudProvider: "aws"

# Cloud region for pricing and API calls
region: "us-east-1"

# Kubernetes cluster name (used for node group discovery)
clusterName: "my-cluster"

# How often to refresh cluster state
# Default: 60s
reconcileInterval: "60s"

# ── Cost Monitor ──────────────────────────────────────────────
costMonitor:
  enabled: true                  # Default: true
  updateInterval: "5m"           # Default: 5m -- how often to recompute costs

# ── Node Autoscaler ──────────────────────────────────────────
nodeAutoscaler:
  enabled: true                  # Default: true
  scanInterval: "30s"            # Default: 30s -- how often to check utilization
  scaleUpThreshold: 80           # Default: 80 -- CPU util % to trigger scale up
  scaleDownThreshold: 30         # Default: 30 -- CPU util % to trigger scale down
  scaleDownDelay: "10m"          # Default: 10m -- wait before scaling down
  maxScaleUpNodes: 5             # Default: 5 -- max nodes to add in one action
  maxScaleDownNodes: 3           # Default: 3 -- max nodes to remove in one action

# ── Node Group Manager ───────────────────────────────────────
nodegroupManager:
  enabled: true                  # Default: true
  minAdjustment:
    enabled: true                # Default: true
    minUtilizationPct: 30        # Default: 30 -- below this, consider reducing min
    observationPeriod: "48h"     # Default: 48h -- how long underutilized before acting
  emptyGroupDetection:
    enabled: true                # Default: true
    emptyPeriod: "336h"          # Default: 336h (14 days) -- how long empty before
                                 #   recommending deletion

# ── Rightsizer (Pod CPU/Memory) ───────────────────────────────
rightsizer:
  enabled: true                  # Default: true
  lookbackWindow: "168h"         # Default: 168h (7 days) -- usage analysis window
  cpuTargetUtilPct: 70           # Default: 70 -- target CPU utilization for requests
  memoryTargetUtilPct: 75        # Default: 75 -- target memory utilization for requests
  minCPURequest: "10m"           # Default: "10m" -- floor for CPU requests
  minMemoryRequest: "32Mi"       # Default: "32Mi" -- floor for memory requests
  oomBumpMultiplier: 2.5         # Default: 2.5 -- multiply memory by this on OOM
  excludeNamespaces:             # Default: ["kube-system"]
    - kube-system

# ── Workload Scaler (Unified HPA+VPA) ────────────────────────
workloadScaler:
  enabled: true                  # Default: true
  verticalEnabled: true          # Default: true -- enable VPA-style scaling
  horizontalEnabled: true        # Default: true -- enable HPA-style scaling
  surgeDetection: true           # Default: true -- detect usage spikes
  surgeThreshold: 2.0            # Default: 2.0 -- 2x normal = surge
  confidenceStartPct: 50         # Default: 50 -- new workloads start at 50% confidence
  confidenceFullDays: 7          # Default: 7 -- full confidence after 7 days of data
  excludeNamespaces:             # Default: ["kube-system", "monitoring"]
    - kube-system
    - monitoring

# ── Evictor (Pod Consolidation) ──────────────────────────────
evictor:
  enabled: true                  # Default: true
  utilizationThreshold: 40       # Default: 40 -- below this %, node is underutilized
  consolidationInterval: "5m"    # Default: 5m -- how often to check for consolidation
  maxConcurrentEvictions: 5      # Default: 5 -- max pods evicted at once
  drainTimeout: "5m"             # Default: 5m -- max time to drain a node
  partialDrainTTL: "30m"         # Default: 30m -- auto-uncordon partially drained nodes
                                 #   after this duration (prevents indefinitely cordoned nodes)
  dryRun: false                  # Default: false -- if true, log but do not evict

# ── Rebalancer ────────────────────────────────────────────────
rebalancer:
  enabled: true                  # Default: true
  schedule: "0 3 * * SUN"       # Default: "0 3 * * SUN" -- cron schedule
  busyRedistribution:
    enabled: true                # Default: true
    overloadedThresholdPct: 90   # Default: 90 -- node above this = overloaded
    targetUtilizationPct: 70     # Default: 70 -- redistribute to bring below this

# ── GPU Optimization ──────────────────────────────────────────
gpu:
  enabled: true                  # Default: true
  idleThresholdPct: 5            # Default: 5 -- GPU util below this = idle
  idleDuration: "30m"            # Default: 30m -- how long idle before acting
  cpuFallbackEnabled: true       # Default: true -- allow CPU workloads on idle GPU nodes

# ── Commitment Reporting (RI/SP/CUD) ─────────────────────────
commitments:
  enabled: true                  # Default: true
  updateInterval: "1h"           # Default: 1h -- how often to refresh from cloud API
  expiryWarningDays:             # Default: [30, 60, 90]
    - 30
    - 60
    - 90

# ── AI Safety Gate ────────────────────────────────────────────
aiGate:
  enabled: true                  # Default: true
  model: "claude-sonnet-4-6"     # Default: "claude-sonnet-4-6"
  timeout: "10s"                 # Default: 10s -- max wait for AI response
  costThresholdUSD: 500          # Default: 500 -- changes above this trigger AI Gate
  scaleThresholdPct: 30          # Default: 30 -- scaling >30% triggers AI Gate
  maxEvictNodes: 3               # Default: 3 -- evicting more nodes than this triggers
                                 #   AI Gate
  timezone: "America/New_York"   # Default: UTC -- IANA timezone for business hours
                                 #   detection in AI Gate prompts

# ── API Server ────────────────────────────────────────────────
apiServer:
  enabled: true                  # Default: true
  address: "0.0.0.0"            # Default: "0.0.0.0"
  port: 8080                     # Default: 8080

# ── Database (SQLite) ────────────────────────────────────────
database:
  path: "/data/koptimizer.db"    # Default: "/data/koptimizer.db" -- SQLite database path
                                 #   for pricing cache, audit log, and metrics. Set to ""
                                 #   to disable persistence (in-memory only).
  retentionDays: 90              # Default: 90 -- days to retain historical data
```

### Validation Rules

The following validation rules are enforced at startup:

- `mode` must be one of: `monitor`, `recommend`, `active`
- `cloudProvider` must be one of: `aws`, `gcp`, `azure` (or empty)
- `scaleUpThreshold` must be greater than `scaleDownThreshold`
- `oomBumpMultiplier` must be >= 1.0
- `surgeThreshold` must be >= 1.0

---

## 5. Operating Modes

KOptimizer has three operating modes that control how aggressively it acts:

| Mode | Cost Monitoring | Recommendations | Auto-Execute |
|------|:--------------:|:---------------:|:------------:|
| `monitor` | Yes | No | No |
| `recommend` | Yes | Yes (CRDs created) | No |
| `active` | Yes | Yes | Yes (for auto-executable actions only) |

### Monitor Mode

The system observes and collects data only. No Recommendation CRDs are created, and no changes are made. Use this mode to:

- Understand your cluster's cost profile before enabling optimizations
- Verify that KOptimizer discovers node groups correctly
- Validate metrics collection is working

### Recommend Mode (Default)

The system generates Recommendation CRDs describing what it would do, but takes no action. Every recommendation includes:

- A human-readable summary
- Step-by-step action plan
- Estimated monthly savings
- Impact assessment (nodes/pods affected, risk level)

Use this mode to build confidence before enabling active optimization.

### Active Mode

The system automatically executes actions that are marked as `AutoExecutable`. Non-auto-executable actions (like instance size changes within a family, or empty node group deletion) are still created as Recommendation CRDs for human approval.

In all modes, the Family-Lock Guard is enforced. Even in `active` mode, cross-family changes are blocked.

### AutoExecutable Decision Table

This table defines which actions are automatically executed in `active` mode and which always require human approval:

| Action | AutoExecutable | Requires AI Gate | Example |
|--------|:-:|:-:|---------|
| Scale node group up (small, <=30%) | Yes | No | ASG desired 3 -> 4 |
| Scale node group up (large, >30%) | Yes | **Yes** | ASG desired 3 -> 6 |
| Scale node group down (small) | Yes | No | ASG desired 5 -> 4 |
| Adjust node group min count | Yes | **Yes** | min 5 -> 0 |
| Patch pod CPU/memory requests | Yes | No | Deployment requests adjusted |
| Evict pods for consolidation (<=3 nodes) | Yes | No | Pods moved to pack tighter |
| Evict pods for consolidation (>3 nodes) | Yes | **Yes** | Large consolidation event |
| Cordon/drain underutilized node | Yes | No | Node emptied, group scaled down |
| GPU taint management | Yes | No | Taint added/removed on idle GPU node |
| HPA+VPA coordination | Yes | No | Scaling targets adjusted |
| Change instance size within family | **No** | N/A | Recommendation: m5.large -> m5.xlarge |
| Delete empty node group | **No** | N/A | Recommendation: delete legacy-workers |
| AI Gate rejection | **No** | N/A | Recommendation with rejection reason |
| Change instance family | **Never generated** | N/A | Blocked by Family-Lock Guard |
| Create new node group | **Never generated** | N/A | Blocked by Family-Lock Guard |

### Switching Modes at Runtime

You can change the operating mode without restarting via the REST API:

```bash
curl -X PUT http://localhost:8080/api/v1/config/mode \
  -H "Content-Type: application/json" \
  -d '{"mode": "active"}'
```

Or via the MCP server using the `set_mode` tool.

---

## 6. Helm Deployment

### Chart Location

The Helm chart is located at `deploy/helm/koptimizer/`.

### Lint Before Installing

```bash
make helm-lint
# or
helm lint deploy/helm/koptimizer
```

### Install

```bash
helm install koptimizer deploy/helm/koptimizer \
  --namespace koptimizer --create-namespace \
  -f my-values.yaml
```

### values.yaml Reference

```yaml
replicaCount: 1

image:
  repository: koptimizer
  tag: latest
  pullPolicy: IfNotPresent

imagePullSecrets: []
nameOverride: ""
fullnameOverride: ""

serviceAccount:
  create: true       # Whether to create a ServiceAccount
  name: ""           # Override name (defaults to chart fullname)
  annotations: {}    # Annotations (e.g., for IAM role binding)

# All KOptimizer config -- see Section 4 for full reference
config:
  mode: "recommend"
  cloudProvider: "aws"
  region: "us-east-1"
  clusterName: ""
  reconcileInterval: "60s"
  # ... (all controller configs -- see Section 4)

# Anthropic API key for AI Safety Gate
# Option 1: Inline (not recommended for production)
aiGateApiKey: ""
# Option 2: Reference an existing Kubernetes Secret (recommended)
aiGateApiKeySecretRef:
  name: ""                    # Name of the Secret
  key: "ANTHROPIC_API_KEY"    # Key within the Secret

service:
  type: ClusterIP
  port: 8080           # REST API port
  metricsPort: 9090    # Prometheus metrics port

resources:
  limits:
    cpu: 500m
    memory: 512Mi
  requests:
    cpu: 100m
    memory: 128Mi

nodeSelector: {}
tolerations: []
affinity: {}

# Prometheus ServiceMonitor (requires prometheus-operator)
serviceMonitor:
  enabled: false
  interval: "30s"
  labels: {}           # Additional labels for ServiceMonitor selection
```

### RBAC Requirements

The Helm chart creates a ClusterRole with the following permissions. If you are managing RBAC manually, ensure these are granted:

| API Group | Resources | Verbs |
|-----------|-----------|-------|
| `""` (core) | nodes, pods, namespaces, services, events | get, list, watch |
| `""` (core) | nodes | patch, update (for cordon/uncordon) |
| `""` (core) | pods/eviction | create |
| `""` (core) | events | create, patch |
| `metrics.k8s.io` | nodes, pods | get, list |
| `apps` | deployments, statefulsets, replicasets, daemonsets | get, list, watch, patch, update |
| `autoscaling` | horizontalpodautoscalers | get, list, watch, create, update, patch, delete |
| `policy` | poddisruptionbudgets | get, list, watch |
| `koptimizer.io` | optimizerconfigs, recommendations, costreports, commitmentreports | get, list, watch, create, update, patch, delete |
| `koptimizer.io` | */status | get, update, patch |
| `coordination.k8s.io` | leases | get, list, watch, create, update, patch, delete |

### Setting Up the Anthropic API Key for AI Gate

**Option 1: Direct value (development only)**

```bash
helm install koptimizer deploy/helm/koptimizer \
  --set aiGateApiKey=sk-ant-api03-xxxxx
```

**Option 2: Kubernetes Secret (recommended for production)**

```bash
# Create the secret first
kubectl create secret generic anthropic-api-key \
  --namespace koptimizer \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-api03-xxxxx

# Reference it in the Helm install
helm install koptimizer deploy/helm/koptimizer \
  --set aiGateApiKeySecretRef.name=anthropic-api-key \
  --set aiGateApiKeySecretRef.key=ANTHROPIC_API_KEY
```

The API key is injected as the `ANTHROPIC_API_KEY` environment variable into the container.

### Enabling/Disabling Controllers

Each controller can be individually enabled or disabled:

```yaml
config:
  costMonitor:
    enabled: true
  nodeAutoscaler:
    enabled: true
  nodegroupManager:
    enabled: false    # Disable node group lifecycle management
  rightsizer:
    enabled: true
  workloadScaler:
    enabled: false    # Disable unified HPA+VPA
  evictor:
    enabled: true
  rebalancer:
    enabled: false    # Disable scheduled rebalancing
  gpu:
    enabled: false    # Disable GPU optimization
  commitments:
    enabled: true
  aiGate:
    enabled: true     # Disable to skip AI validation entirely
```

### ServiceMonitor for Prometheus

If you are running the Prometheus Operator, enable the ServiceMonitor:

```yaml
serviceMonitor:
  enabled: true
  interval: "30s"
  labels:
    release: prometheus    # Match your Prometheus label selector
```

This creates a ServiceMonitor that scrapes the `:9090/metrics` endpoint.

---

## 7. REST API Reference

The REST API is served on the address and port configured under `apiServer` (default: `0.0.0.0:8080`). All endpoints are under the `/api/v1` prefix. Responses are JSON.

### Cluster

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/cluster/summary` | Cluster overview: node count, pod count, CPU/memory utilization, estimated monthly cost |
| `GET` | `/api/v1/cluster/health` | KOptimizer system health and enabled/disabled state of each controller |

**Example:**

```bash
# Get cluster summary
curl -s http://localhost:8080/api/v1/cluster/summary | jq .

# Get system health
curl -s http://localhost:8080/api/v1/cluster/health | jq .
```

### Node Groups

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/nodegroups` | All node groups with instance types, current/min/max counts, utilization, monthly cost |
| `GET` | `/api/v1/nodegroups/{id}` | Single node group detail |
| `GET` | `/api/v1/nodegroups/{id}/nodes` | Nodes belonging to a node group with per-node CPU/memory, cost, pod count |
| `GET` | `/api/v1/nodegroups/empty` | Node groups with zero running pods (candidates for removal) |

**Example:**

```bash
# List all node groups
curl -s http://localhost:8080/api/v1/nodegroups | jq .

# Get details for a specific node group
curl -s http://localhost:8080/api/v1/nodegroups/asg-workers-m5 | jq .

# List nodes in a node group
curl -s http://localhost:8080/api/v1/nodegroups/asg-workers-m5/nodes | jq .

# Find empty node groups
curl -s http://localhost:8080/api/v1/nodegroups/empty | jq .
```

### Nodes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/nodes` | All nodes with instance type, node group, CPU/memory capacity and usage, cost, spot status, GPU status |
| `GET` | `/api/v1/nodes/{name}` | Single node detail with full resource breakdown and metadata |

**Example:**

```bash
# List all nodes
curl -s http://localhost:8080/api/v1/nodes | jq .

# Get details for a specific node
curl -s http://localhost:8080/api/v1/nodes/ip-10-0-1-42.ec2.internal | jq .
```

### Cost

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/cost/summary` | Total and projected monthly cost in USD, node count |
| `GET` | `/api/v1/cost/by-namespace` | Cost breakdown by Kubernetes namespace |
| `GET` | `/api/v1/cost/by-workload` | Cost breakdown by workload (deployment, statefulset, etc.) |
| `GET` | `/api/v1/cost/by-label` | Cost breakdown by label, useful for team/project allocation |
| `GET` | `/api/v1/cost/trend` | Historical cost trend data over time |
| `GET` | `/api/v1/cost/savings` | Potential cost savings with breakdown by category |

**Example:**

```bash
# Get cost summary
curl -s http://localhost:8080/api/v1/cost/summary | jq .

# Get cost by namespace
curl -s http://localhost:8080/api/v1/cost/by-namespace | jq .

# Get savings opportunities
curl -s http://localhost:8080/api/v1/cost/savings | jq .
```

### Commitments

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/commitments` | All commitment instruments (RIs, Savings Plans, CUDs, Reservations) with utilization |
| `GET` | `/api/v1/commitments/underutilized` | Commitments with low utilization (wasted spend) |
| `GET` | `/api/v1/commitments/expiring` | Commitments expiring soon (within configured warning days) |

**Example:**

```bash
# List all commitments
curl -s http://localhost:8080/api/v1/commitments | jq .

# Find underutilized commitments (wasted money)
curl -s http://localhost:8080/api/v1/commitments/underutilized | jq .

# Find expiring commitments
curl -s http://localhost:8080/api/v1/commitments/expiring | jq .
```

### Recommendations

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/recommendations` | All optimization recommendations (filter with `?type=` and `?status=`) |
| `GET` | `/api/v1/recommendations/summary` | Summary: total count, breakdown by type, total potential savings |
| `GET` | `/api/v1/recommendations/{id}` | Single recommendation detail including AI Gate result if applicable |
| `POST` | `/api/v1/recommendations/{id}/approve` | Approve a recommendation for execution |
| `POST` | `/api/v1/recommendations/{id}/dismiss` | Dismiss a recommendation (will not be applied) |

**Example:**

```bash
# List all recommendations
curl -s http://localhost:8080/api/v1/recommendations | jq .

# Get recommendations summary
curl -s http://localhost:8080/api/v1/recommendations/summary | jq .

# Get a specific recommendation
curl -s http://localhost:8080/api/v1/recommendations/rec-abc123 | jq .

# Approve a recommendation
curl -s -X POST http://localhost:8080/api/v1/recommendations/rec-abc123/approve | jq .

# Dismiss a recommendation
curl -s -X POST http://localhost:8080/api/v1/recommendations/rec-abc123/dismiss | jq .
```

### Workloads

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/workloads` | All workloads grouped by owner with replica count and total CPU/memory |
| `GET` | `/api/v1/workloads/{ns}/{kind}/{name}` | Single workload detail |
| `GET` | `/api/v1/workloads/{ns}/{kind}/{name}/rightsizing` | Rightsizing recommendations for a workload |
| `GET` | `/api/v1/workloads/{ns}/{kind}/{name}/scaling` | Scaling recommendations (HPA+VPA status) for a workload |

**Example:**

```bash
# List all workloads
curl -s http://localhost:8080/api/v1/workloads | jq .

# Get details for a specific workload
curl -s http://localhost:8080/api/v1/workloads/default/Deployment/my-app | jq .

# Get rightsizing recommendation for a workload
curl -s http://localhost:8080/api/v1/workloads/default/Deployment/my-app/rightsizing | jq .

# Get scaling recommendation for a workload
curl -s http://localhost:8080/api/v1/workloads/default/Deployment/my-app/scaling | jq .
```

### GPU

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/gpu/nodes` | All GPU nodes with GPU count, GPU usage, CPU/memory utilization, hourly cost |
| `GET` | `/api/v1/gpu/utilization` | Aggregate GPU utilization: total GPUs, used GPUs, utilization percentage |
| `GET` | `/api/v1/gpu/recommendations` | GPU-specific optimization recommendations |

**Example:**

```bash
# List GPU nodes
curl -s http://localhost:8080/api/v1/gpu/nodes | jq .

# Get aggregate GPU utilization
curl -s http://localhost:8080/api/v1/gpu/utilization | jq .

# Get GPU recommendations
curl -s http://localhost:8080/api/v1/gpu/recommendations | jq .
```

### Config

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/config` | Current KOptimizer configuration |
| `PUT` | `/api/v1/config/mode` | Set operating mode (`monitor`, `recommend`, `active`) |

**Example:**

```bash
# Get current config
curl -s http://localhost:8080/api/v1/config | jq .

# Switch to active mode
curl -s -X PUT http://localhost:8080/api/v1/config/mode \
  -H "Content-Type: application/json" \
  -d '{"mode": "active"}' | jq .

# Switch to monitor mode
curl -s -X PUT http://localhost:8080/api/v1/config/mode \
  -H "Content-Type: application/json" \
  -d '{"mode": "monitor"}' | jq .
```

---

## 8. MCP Server

### What is MCP?

The Model Context Protocol (MCP) is an open standard that allows AI assistants (like Claude) to interact with external tools and data sources. KOptimizer ships an MCP server that exposes all of its functionality as tools that Claude can call directly.

This means you can manage your Kubernetes cluster's cost optimization through natural conversation with Claude -- asking questions like "What are my most expensive namespaces?" or "Show me idle GPU nodes" and getting real answers from your live cluster.

### Building the MCP Server

```bash
make build-mcp
# Output: bin/koptimizer-mcp
```

The MCP server binary communicates with the KOptimizer REST API over HTTP, so the main KOptimizer process must be running and accessible.

### Running the MCP Server

```bash
# Connect to a local KOptimizer instance
./bin/koptimizer-mcp --api-url http://localhost:8080

# Connect to a KOptimizer instance in the cluster (via port-forward)
kubectl port-forward -n koptimizer svc/koptimizer 8080:8080 &
./bin/koptimizer-mcp --api-url http://localhost:8080
```

The MCP server communicates over stdin/stdout using JSON-RPC (the MCP protocol). All log output goes to stderr to keep the JSON-RPC channel clean.

### Connecting to Claude Desktop

Add the following to your Claude Desktop configuration file:

- **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "koptimizer": {
      "command": "/path/to/koptimizer-mcp",
      "args": ["--api-url", "http://localhost:8080"]
    }
  }
}
```

Replace `/path/to/koptimizer-mcp` with the actual path to the binary (e.g., `/usr/local/bin/koptimizer-mcp` or the path from `make build-mcp`).

If KOptimizer is running in-cluster, set up a port-forward first, or use the cluster service URL if the MCP server has network access to the cluster.

### Connecting to Claude Code

Claude Code discovers MCP servers from its configuration. Add the koptimizer MCP server to your project or global configuration:

```bash
# Add to the current project
claude mcp add koptimizer -- /path/to/koptimizer-mcp --api-url http://localhost:8080
```

### List of All 31 MCP Tools

#### Cluster (2 tools)

| Tool | Description |
|------|-------------|
| `get_cluster_summary` | Get a high-level summary of the cluster including node count, pod count, CPU/memory utilization, and estimated monthly cost |
| `get_cluster_health` | Get health status of KOptimizer and the enabled/disabled state of each controller |

#### Node Groups (4 tools)

| Tool | Description |
|------|-------------|
| `list_nodegroups` | List all node groups with instance types, current/min/max counts, utilization, and monthly cost |
| `get_nodegroup` | Get detailed information about a specific node group by ID |
| `get_nodegroup_nodes` | List all nodes belonging to a specific node group |
| `list_empty_nodegroups` | List node groups with zero running pods |

#### Nodes (2 tools)

| Tool | Description |
|------|-------------|
| `list_nodes` | List all nodes with instance type, node group, CPU/memory, cost, spot status, GPU status |
| `get_node` | Get detailed information about a specific node by name |

#### Cost (6 tools)

| Tool | Description |
|------|-------------|
| `get_cost_summary` | Get overall cost summary including total and projected monthly cost |
| `get_cost_by_namespace` | Get cost breakdown by Kubernetes namespace |
| `get_cost_by_workload` | Get cost breakdown by workload |
| `get_cost_by_label` | Get cost breakdown by label |
| `get_cost_trend` | Get historical cost trend data |
| `get_cost_savings` | Get potential cost savings opportunities |

#### Commitments (3 tools)

| Tool | Description |
|------|-------------|
| `list_commitments` | List all commitment instruments (RIs, Savings Plans, CUDs, Reservations) |
| `list_underutilized_commitments` | List commitments with low utilization |
| `list_expiring_commitments` | List commitments expiring soon |

#### Recommendations (5 tools)

| Tool | Description |
|------|-------------|
| `list_recommendations` | List all optimization recommendations |
| `get_recommendation` | Get details of a specific recommendation by ID |
| `approve_recommendation` | Approve a recommendation for execution |
| `dismiss_recommendation` | Dismiss a recommendation |
| `get_recommendations_summary` | Get summary with counts by type and total potential savings |

#### Workloads (4 tools)

| Tool | Description |
|------|-------------|
| `list_workloads` | List all workloads with replica count and resource usage |
| `get_workload` | Get details of a specific workload by namespace, kind, and name |
| `get_workload_rightsizing` | Get rightsizing recommendations for a specific workload |
| `get_workload_scaling` | Get scaling recommendations for a specific workload |

#### GPU (3 tools)

| Tool | Description |
|------|-------------|
| `list_gpu_nodes` | List all GPU nodes with GPU count, usage, and cost |
| `get_gpu_utilization` | Get aggregate GPU utilization across the cluster |
| `list_gpu_recommendations` | List GPU-specific optimization recommendations |

#### Config (2 tools)

| Tool | Description |
|------|-------------|
| `get_config` | Get the current KOptimizer configuration |
| `set_mode` | Set the operating mode (monitor, recommend, active) |

### Example Interactions

Once connected, you can interact with Claude naturally:

> **You:** What does our cluster cost look like? Any easy savings?
>
> **Claude:** *(calls get_cost_summary, then get_cost_savings)*
> Your cluster is running 47 nodes with an estimated monthly cost of $12,340. I found $2,100/month in potential savings:
> - $890/month from rightsizing 12 over-provisioned deployments
> - $720/month from consolidating 3 underutilized nodes
> - $490/month from an underutilized m5.2xlarge Reserved Instance...

> **You:** Show me any idle GPU nodes and what we can do about them.
>
> **Claude:** *(calls list_gpu_nodes, then list_gpu_recommendations)*
> You have 2 GPU nodes that are idle...

> **You:** Approve recommendation rec-abc123
>
> **Claude:** *(calls approve_recommendation with id "rec-abc123")*
> Done. The recommendation to scale down node group workers-m5 from 8 to 6 nodes has been approved and will be executed.

---

## 9. Cloud Provider Setup

### AWS (EKS)

#### Required IAM Permissions

Attach the following IAM policy to the instance role or IRSA role used by KOptimizer:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "EC2ReadOnly",
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeInstances",
        "ec2:DescribeInstanceTypes",
        "ec2:DescribeRegions"
      ],
      "Resource": "*"
    },
    {
      "Sid": "ASGOperations",
      "Effect": "Allow",
      "Action": [
        "autoscaling:DescribeAutoScalingGroups",
        "autoscaling:DescribeTags",
        "autoscaling:UpdateAutoScalingGroup",
        "autoscaling:SetDesiredCapacity"
      ],
      "Resource": "*"
    },
    {
      "Sid": "PricingRead",
      "Effect": "Allow",
      "Action": [
        "pricing:GetProducts",
        "pricing:DescribeServices"
      ],
      "Resource": "*"
    },
    {
      "Sid": "CostExplorerRead",
      "Effect": "Allow",
      "Action": [
        "ce:GetCostAndUsage",
        "ce:GetReservationUtilization",
        "ce:GetSavingsPlansUtilization"
      ],
      "Resource": "*"
    },
    {
      "Sid": "CommitmentsRead",
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeReservedInstances",
        "savingsplans:DescribeSavingsPlans",
        "savingsplans:DescribeSavingsPlansOfferingRates"
      ],
      "Resource": "*"
    }
  ]
}
```

For IRSA (recommended), annotate the ServiceAccount:

```yaml
serviceAccount:
  create: true
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/koptimizer-role
```

#### ASG Tag Requirements

KOptimizer discovers node groups by reading Auto Scaling Group tags. Ensure your ASGs have the standard EKS tag:

```
kubernetes.io/cluster/<cluster-name> = owned
```

This tag is automatically present on ASGs created by `eksctl` or EKS managed node groups.

### GCP (GKE)

#### Required Roles

Grant the following roles to the KOptimizer service account (GKE Workload Identity is recommended):

- `roles/compute.viewer` -- Read MIGs, instances, instance types
- `roles/compute.instanceAdmin.v1` -- Scale MIGs (set target size)
- `roles/billing.viewer` -- Read billing data for cost monitoring
- `roles/cloudasset.viewer` -- Read Committed Use Discounts

For Workload Identity:

```bash
gcloud iam service-accounts add-iam-policy-binding \
  koptimizer@PROJECT_ID.iam.gserviceaccount.com \
  --role roles/iam.workloadIdentityUser \
  --member "serviceAccount:PROJECT_ID.svc.id.goog[koptimizer/koptimizer]"
```

Then annotate the Kubernetes ServiceAccount:

```yaml
serviceAccount:
  create: true
  annotations:
    iam.gke.io/gcp-service-account: koptimizer@PROJECT_ID.iam.gserviceaccount.com
```

#### MIG Label Requirements

KOptimizer discovers node pools by reading labels on Managed Instance Groups. GKE automatically labels MIGs with:

```
goog-k8s-cluster-name = <cluster-name>
goog-k8s-node-pool-name = <pool-name>
```

No additional labeling is required for standard GKE node pools.

### Azure (AKS)

#### Required Permissions

Assign the following roles to the KOptimizer managed identity:

- `Reader` on the resource group containing the AKS cluster
- `Virtual Machine Contributor` on the resource group (for scaling VMSS)
- `Cost Management Reader` on the subscription (for cost data)
- `Reservations Reader` on the subscription (for reservation data)

For Pod Identity or Workload Identity Federation:

```bash
az role assignment create \
  --assignee <koptimizer-identity-client-id> \
  --role "Virtual Machine Contributor" \
  --scope /subscriptions/<sub-id>/resourceGroups/<rg-name>
```

#### VMSS Tag Requirements

KOptimizer discovers node pools by reading tags on Virtual Machine Scale Sets. AKS automatically tags VMSS with:

```
aks-managed-cluster-name = <cluster-name>
aks-managed-poolName = <pool-name>
```

No additional tagging is required for standard AKS node pools.

---

## 10. Monitoring

### Prometheus Metrics

KOptimizer exports the following metrics on the `:9090/metrics` endpoint:

#### Cluster-Level Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `koptimizer_cluster_node_count` | Gauge | Total number of nodes in the cluster |
| `koptimizer_cluster_monthly_cost_usd` | Gauge | Estimated monthly cost of the cluster in USD |
| `koptimizer_cluster_potential_savings_usd` | Gauge | Potential monthly savings in USD |

#### Node Group Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `koptimizer_nodegroup_desired_count` | Gauge | `nodegroup`, `instance_type`, `family` | Desired node count per node group |
| `koptimizer_nodegroup_cpu_utilization_pct` | Gauge | `nodegroup` | CPU utilization percentage per node group |
| `koptimizer_nodegroup_memory_utilization_pct` | Gauge | `nodegroup` | Memory utilization percentage per node group |

#### Recommendation Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `koptimizer_recommendations_total` | Counter | `type`, `priority` | Total recommendations generated |
| `koptimizer_recommendations_executed_total` | Counter | `type` | Total recommendations auto-executed |

#### AI Safety Gate Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `koptimizer_aigate_validations_total` | Counter | `result` (`approved`, `rejected`, `error`) | Total AI Gate validations |
| `koptimizer_aigate_latency_seconds` | Histogram | -- | AI Gate validation latency |

#### Family-Lock Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `koptimizer_familylock_blocked_total` | Counter | `action` | Total operations blocked by family lock |

#### Commitment Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `koptimizer_commitment_utilization_pct` | Gauge | `id`, `type`, `instance_family` | Utilization percentage per commitment |
| `koptimizer_commitment_wasted_monthly_usd` | Gauge | `id`, `type` | Monthly wasted cost per underutilized commitment |

#### Evictor Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `koptimizer_evictions_total` | Counter | Total pod evictions for consolidation |
| `koptimizer_nodes_consolidated_total` | Counter | Total nodes consolidated (drained) |

#### GPU Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `koptimizer_gpu_nodes_idle` | Gauge | Number of GPU nodes currently idle |
| `koptimizer_gpu_nodes_cpu_fallback` | Gauge | Number of GPU nodes serving CPU workloads as fallback |
| `koptimizer_gpu_nodes_cpu_scavenging` | Gauge | Number of GPU nodes with active CPU scavenging |

#### Spot Instance Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `koptimizer_spot_nodes_total` | Gauge | Total number of spot instance nodes |
| `koptimizer_spot_savings_monthly_usd` | Gauge | Monthly savings from spot instances vs on-demand |
| `koptimizer_spot_interruptions_total` | Counter | Total spot instance interruptions handled |
| `koptimizer_spot_fallbacks_total` | Counter | Total fallbacks from spot to on-demand |

#### Hibernation Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `koptimizer_hibernated_nodegroups` | Gauge | Number of currently hibernated node groups |
| `koptimizer_hibernation_savings_monthly_usd` | Gauge | Estimated monthly savings from hibernation |

#### Storage Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `koptimizer_storage_pvc_count` | Gauge | Total number of PersistentVolumeClaims |
| `koptimizer_storage_overprovisioned_pvcs` | Gauge | Number of overprovisioned PVCs |
| `koptimizer_storage_unused_pvcs` | Gauge | Number of unused PVCs |
| `koptimizer_storage_monthly_cost_usd` | Gauge | Estimated monthly cost of persistent storage |

#### Network Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `koptimizer_network_cross_az_bytes_total` | Counter | `source_az`, `dest_az` | Total cross-AZ network traffic in bytes |
| `koptimizer_network_cross_az_monthly_cost_usd` | Gauge | -- | Estimated monthly cost of cross-AZ traffic |

#### Pricing Fallback Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `koptimizer_pricing_fallback_total` | Counter | `provider`, `region` | Total times fallback (hardcoded) pricing was used instead of live API |
| `koptimizer_pricing_fallback_active` | Gauge | `provider`, `region` | Set to 1 when fallback pricing is currently in use |
| `koptimizer_pricing_last_live_update_timestamp` | Gauge | `provider`, `region` | Unix timestamp of last successful live pricing API update |

#### Alert Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `koptimizer_alerts_fired_total` | Counter | `type`, `severity` | Total alerts fired |

### Key Metrics to Alert On

| Alert | Condition | Severity | Reason |
|-------|-----------|----------|--------|
| **High cluster cost** | `koptimizer_cluster_monthly_cost_usd > budget_threshold` | Warning | Cluster costs exceeding budget |
| **AI Gate errors** | `rate(koptimizer_aigate_validations_total{result="error"}[5m]) > 0` | Warning | AI Gate API failures (changes falling back to recommendations) |
| **AI Gate high latency** | `koptimizer_aigate_latency_seconds{quantile="0.99"} > 8` | Warning | AI Gate approaching timeout |
| **Family lock violations** | `rate(koptimizer_familylock_blocked_total[1h]) > 0` | Info | Something attempted a cross-family change (blocked) |
| **Commitment waste** | `sum(koptimizer_commitment_wasted_monthly_usd) > 500` | Warning | Significant money wasted on unused commitments |
| **Low commitment utilization** | `koptimizer_commitment_utilization_pct < 50` | Warning | Commitment below 50% utilization |
| **Excessive evictions** | `rate(koptimizer_evictions_total[10m]) > 10` | Critical | High eviction rate may indicate instability |
| **GPU waste** | `koptimizer_gpu_nodes_idle > 2` | Warning | Multiple GPU nodes sitting idle |
| **Pricing fallback active** | `koptimizer_pricing_fallback_active > 0` | Warning | Live pricing API unavailable, using stale fallback rates |
| **Pricing data stale** | `time() - koptimizer_pricing_last_live_update_timestamp > 86400` | Warning | No live pricing update in 24+ hours |
| **Spot interruptions** | `rate(koptimizer_spot_interruptions_total[1h]) > 5` | Warning | High spot interruption rate |

### Grafana Dashboard Suggestions

Create dashboards for the following views:

**1. Cost Overview Dashboard**
- Total monthly cost gauge (`koptimizer_cluster_monthly_cost_usd`)
- Potential savings gauge (`koptimizer_cluster_potential_savings_usd`)
- Cost trend over time (from the `/api/v1/cost/trend` endpoint or CostReport CRDs)
- Commitment utilization heatmap

**2. Node Group Health Dashboard**
- Node count per group (`koptimizer_nodegroup_desired_count`)
- CPU utilization per group (`koptimizer_nodegroup_cpu_utilization_pct`)
- Memory utilization per group (`koptimizer_nodegroup_memory_utilization_pct`)
- Scaling events timeline (from `koptimizer_recommendations_executed_total`)

**3. Safety Dashboard**
- AI Gate approval/rejection pie chart (`koptimizer_aigate_validations_total`)
- AI Gate latency histogram (`koptimizer_aigate_latency_seconds`)
- Family lock blocked events (`koptimizer_familylock_blocked_total`)
- Eviction rate (`koptimizer_evictions_total`)

**4. GPU Dashboard**
- Idle GPU nodes (`koptimizer_gpu_nodes_idle`)
- CPU fallback nodes (`koptimizer_gpu_nodes_cpu_fallback`)
- GPU utilization from the `/api/v1/gpu/utilization` endpoint

**5. Pricing Health Dashboard**
- Fallback pricing active indicator (`koptimizer_pricing_fallback_active`)
- Time since last live update (`koptimizer_pricing_last_live_update_timestamp`)
- Fallback events over time (`koptimizer_pricing_fallback_total`)

**6. Spot Instance Dashboard**
- Spot node count (`koptimizer_spot_nodes_total`)
- Monthly spot savings (`koptimizer_spot_savings_monthly_usd`)
- Interruption rate (`koptimizer_spot_interruptions_total`)
- Fallback events (`koptimizer_spot_fallbacks_total`)

---

## 11. Safety Guarantees

KOptimizer is designed with multiple layers of safety to prevent destructive changes.

### What the Family-Lock Guard Prevents

The Family-Lock Guard is the core safety mechanism. It sits between every node operation and the cloud/Kubernetes API:

| Action | Result | Reasoning |
|--------|--------|-----------|
| Change instance family (e.g., m5 -> c5) | **ALWAYS BLOCKED** | Breaks Reserved Instance/Savings Plan commitments |
| Create a new node group | **ALWAYS BLOCKED** | Creates complexity, introduces unmanaged families |
| Change a node group's instance type | **ALWAYS BLOCKED** | Could break workload compatibility |
| Scale existing node group (same type) | Allowed | Just changes desired count |
| Adjust node group min/max counts | Allowed | Parameter adjustment, not type change |
| Recommend size change within family | Allowed (recommendation only) | e.g., m5.large -> m5.xlarge; human must approve |

The guard extracts the instance family from the instance type (e.g., `m5` from `m5.xlarge`) and compares families before any operation proceeds. If a family mismatch is detected, the operation is blocked and logged. The `koptimizer_familylock_blocked_total` metric is incremented.

### AI Safety Gate Triggers and Fallback Behavior

The AI Safety Gate calls Claude to validate changes that meet any of these criteria:

| Trigger | Threshold | Configurable |
|---------|-----------|:------------:|
| Scaling a node group by more than N% | `scaleThresholdPct` (default: 30%) | Yes |
| Setting a node group min to 0 | Always triggers | No |
| Recommending node group deletion | Always triggers | No |
| Evicting pods from more than N nodes simultaneously | `maxEvictNodes` (default: 3) | Yes |
| Cost impact exceeding $N/month | `costThresholdUSD` (default: $500) | Yes |

**AI Gate decision flow:**

1. The AI Gate receives the full context: current cluster state, the proposed change, risk factors, and historical scaling patterns.
2. Claude evaluates the change and returns: approved/rejected, confidence score, reasoning, warnings, and an alternative suggestion if rejected.
3. If **approved**: the change proceeds automatically.
4. If **rejected**: the change becomes a Recommendation CRD that a human must approve.
5. If **API error or timeout** (10s default): the change falls back to Recommendation CRD.

All AI Gate decisions are logged to the Recommendation CRD for full audit trail, including the AI's reasoning.

### PDB Awareness During Evictions

The Evictor controller respects Pod Disruption Budgets (PDBs) during all eviction operations:

- Before evicting any pod, the controller checks the PDB for the pod's owner.
- If evicting the pod would violate the PDB's `minAvailable` or `maxUnavailable` constraint, the eviction is skipped.
- The drainer performs safe node drain with configurable grace periods.
- Pods with `local-storage` are not evicted by default (they cannot be rescheduled).

### Additional Safety Measures

- **Leader election**: Only one KOptimizer instance is active at a time (via `coordination.k8s.io/leases`), preventing duplicate actions.
- **Reconcile interval**: State is refreshed periodically (default: 60s), not reactively, preventing cascading actions.
- **Scale-down delay**: The node autoscaler waits for `scaleDownDelay` (default: 10m) before scaling down, preventing flapping.
- **Dry-run mode**: The evictor supports `dryRun: true` to log consolidation plans without executing them.
- **Confidence scoring**: The workload scaler starts new workloads at 50% confidence and ramps to 100% over 7 days, preventing aggressive rightsizing on new deployments.
- **OOM protection**: When an OOM kill is detected, memory requests are bumped by `oomBumpMultiplier` (default: 2.5x) to prevent repeated crashes.
- **Partial drain auto-recovery**: Nodes that are only partially drained (some pods failed to evict) are automatically uncordoned after `partialDrainTTL` (default: 30m), preventing indefinitely cordoned nodes from reducing cluster capacity.
- **Per-provider spot discount estimation**: Spot cost calculations use per-instance-family discount estimates from each cloud provider rather than a flat multiplier, giving more accurate savings reports.
- **Background pricing refresh**: The pricing cache is proactively refreshed every 45 minutes (AWS) to avoid latency spikes when cache expires.
- **Pricing sanity checks**: All live pricing data is validated against reasonable bounds ($0.001-$200/hr) before caching, preventing absurd values from corrupting cost calculations.
- **Pagination safety limits**: All cloud API pagination loops have bounded iteration limits (50-200 pages) to prevent runaway requests from a misbehaving API.

---

## 12. Troubleshooting

### Common Issues and Solutions

#### KOptimizer pod is not starting

**Symptom**: Pod stuck in `CrashLoopBackOff` or `Error` state.

**Check**:
```bash
kubectl logs -n koptimizer deploy/koptimizer
```

**Common causes**:
- Invalid configuration: Check for validation errors in the log output (e.g., `scaleUpThreshold must be greater than scaleDownThreshold`).
- Missing cloud credentials: Ensure the ServiceAccount has proper IAM/GCP/Azure role bindings.
- Missing CRDs: Run `make generate` and apply the CRDs from `deploy/helm/koptimizer/templates/crds/`.

#### No node groups discovered

**Symptom**: `list_nodegroups` returns empty, no scaling actions occur.

**Check**:
```bash
curl -s http://localhost:8080/api/v1/nodegroups | jq .
```

**Common causes**:
- AWS: ASGs missing the `kubernetes.io/cluster/<cluster-name>` tag.
- GCP: MIGs missing the `goog-k8s-cluster-name` label.
- Azure: VMSS missing the `aks-managed-cluster-name` tag.
- Wrong `cloudProvider` or `region` in config.
- Cloud provider IAM permissions insufficient.

#### AI Safety Gate always falling back to recommendations

**Symptom**: All large changes become recommendations instead of being auto-executed.

**Check**:
```bash
# Check AI Gate health
curl -s http://localhost:8080/api/v1/cluster/health | jq .aiGate

# Check AI Gate metrics
curl -s http://localhost:9090/metrics | grep koptimizer_aigate
```

**Common causes**:
- `ANTHROPIC_API_KEY` environment variable not set or invalid.
- AI Gate timeout too low (increase `aiGate.timeout` if Claude is slow to respond).
- Network policy blocking outbound HTTPS to `api.anthropic.com`.
- `aiGate.enabled` is `false` in config.

#### Recommendations not being generated

**Symptom**: Mode is `recommend` or `active`, but no Recommendation CRDs appear.

**Check**:
```bash
kubectl get recommendations.koptimizer.io -A
curl -s http://localhost:8080/api/v1/recommendations | jq .
```

**Common causes**:
- Mode is `monitor` (no recommendations in monitor mode).
- All controllers are disabled.
- Cluster is already well-optimized (no recommendations to make).
- Metrics server not running (KOptimizer needs `metrics.k8s.io` API for utilization data).

#### Workload rightsizing not working

**Symptom**: No rightsizing recommendations appear for workloads.

**Check**:
```bash
curl -s http://localhost:8080/api/v1/workloads/default/Deployment/my-app/rightsizing | jq .
```

**Common causes**:
- Namespace is in `excludeNamespaces` list.
- Not enough data yet (need `lookbackWindow` worth of metrics, default: 7 days).
- Workload resource usage is already close to target utilization percentages.
- Metrics server or Prometheus not providing pod-level metrics.

#### Pricing data is stale or using fallback rates

**Symptom**: `koptimizer_pricing_fallback_active` metric is 1, or cost estimates seem inaccurate.

**Check**:
```bash
# Check pricing fallback status
curl -s http://localhost:9090/metrics | grep koptimizer_pricing

# Check last live update time
curl -s http://localhost:9090/metrics | grep pricing_last_live_update
```

**Common causes**:
- AWS: IAM policy missing `pricing:GetProducts` permission. The AWS Pricing API is only available in `us-east-1`.
- GCP: Cloud Billing API not enabled, or service account missing `roles/billing.viewer`.
- Azure: Public Retail Prices API is unreachable (network policy).
- All providers have hardcoded fallback rates that are updated quarterly. If live API is unavailable, cost estimates may drift.

**Solution**: Fix the IAM/network issue to restore live pricing. KOptimizer will automatically switch back from fallback to live data on the next pricing cache refresh (every 45 minutes for background refresh, or 1 hour for lazy refresh).

#### Pods being evicted unexpectedly

**Symptom**: Pods are being evicted in `active` mode.

**Check**:
```bash
# Check recent evictions
kubectl get events -A --field-selector reason=Evicted

# Check evictor config
curl -s http://localhost:8080/api/v1/config | jq .evictor
```

**Solutions**:
- Set `evictor.dryRun: true` to see what would be evicted without acting.
- Increase `evictor.utilizationThreshold` to be less aggressive.
- Decrease `evictor.maxConcurrentEvictions` to limit blast radius.
- Add PDBs to critical workloads to prevent disruption.
- Switch to `recommend` mode to review eviction plans before approving.

### Debug Logging

Enable debug logging by passing zap flags:

```bash
# For local development
./bin/koptimizer --config config.yaml --zap-log-level debug

# For Helm deployment, add to the container args
# In values.yaml or via --set:
# No direct values.yaml support; add to deployment template args:
#   - --zap-log-level=debug
```

Log levels: `debug`, `info` (default), `warn`, `error`.

### Checking Controller Health via API

The health endpoint provides a quick way to verify all components:

```bash
# System health (KOptimizer controllers)
curl -s http://localhost:8080/api/v1/cluster/health | jq .

# Kubernetes health probes
curl -s http://localhost:8081/healthz    # Liveness
curl -s http://localhost:8081/readyz     # Readiness
```

### Port-Forwarding for Debugging

When running in-cluster, use port-forwarding to access the API and metrics:

```bash
# REST API
kubectl port-forward -n koptimizer svc/koptimizer 8080:8080

# Prometheus metrics
kubectl port-forward -n koptimizer svc/koptimizer 9090:9090

# Health probes
kubectl port-forward -n koptimizer deploy/koptimizer 8081:8081
```

### Verifying the Build

```bash
# Run all checks
make all    # clean + generate + build + build-mcp + test

# Individual checks
make build         # Compile main binary
make build-mcp     # Compile MCP server binary
make test          # Run all tests with coverage
make lint          # Run golangci-lint
make helm-lint     # Validate Helm chart
make generate      # Regenerate CRDs and deepcopy
```
