# KOptimizer

Kubernetes cost optimization engine for AWS (EKS), GCP (GKE), and Azure (AKS). Automatically reduces cloud spend while preserving cluster stability through family-locked node scaling, workload rightsizing, and AI-validated safety gates.

## What It Does

| Feature | Description |
|---------|-------------|
| **Cost Monitoring** | Real-time cost visibility by namespace, workload, and label |
| **Node Autoscaling** | Scale existing node groups up/down within the same instance family |
| **Node Group Lifecycle** | Adjust min/max counts, detect empty groups |
| **Workload Rightsizing** | Auto-apply optimal CPU/memory requests based on actual usage |
| **Workload Autoscaler** | Unified HPA+VPA that resolves scaling conflicts |
| **Evictor / Bin-Packing** | Consolidate pods onto fewer nodes, drain underutilized nodes |
| **Rebalancer** | Redistribute workloads for optimal packing on a schedule |
| **GPU Optimization** | Detect idle GPUs, run CPU workloads on idle GPU nodes |
| **Spot Management** | Spot/preemptible instance pricing, diversity, and interruption handling |
| **Commitment Reporting** | Track RI/Savings Plan/CUD utilization and expiry |
| **Hibernation** | Schedule-based cluster hibernation for dev/staging environments |
| **AI Safety Gate** | Claude validates risky changes before execution |

### Key Design Principles

- **Family-Lock Guard** — Never changes instance families or creates new node groups. Scales within existing families only, preserving Reserved Instance and Savings Plan commitments.
- **AI Safety Gate** — Large or risky changes are validated by Claude before execution. If rejected or if the API is unreachable, changes fall back to human-approved recommendations.
- **Three Operating Modes** — `monitor` (observe only), `recommend` (generate recommendations), `active` (auto-execute safe changes).

## Prerequisites

- Go 1.22+
- Kubernetes cluster (EKS, GKE, or AKS)
- kubectl configured with cluster access
- Helm 3 (for deployment)
- Docker (optional, for container images)

## Build

```bash
# Build all binaries
make build          # bin/koptimizer     (main controller)
make build-mcp      # bin/koptimizer-mcp (MCP server for Claude)
make build-dashboard # bin/koptimizer-dash (web dashboard)

# Run tests
make test

# Build Docker image
make docker-build

# Build everything (clean + generate + build + test)
make all
```

## Run Locally

```bash
# Ensure KUBECONFIG is set
export KUBECONFIG=~/.kube/config

# Run with default config
./bin/koptimizer --config config.yaml

# Run with custom addresses
./bin/koptimizer \
  --config config.yaml \
  --metrics-bind-address :9090 \
  --health-probe-bind-address :8081
```

### Minimal Config (`config.yaml`)

```yaml
mode: "recommend"
cloudProvider: "aws"      # aws | gcp | azure
region: "us-east-1"

aiGate:
  enabled: false          # set true + ANTHROPIC_API_KEY env var to enable

apiServer:
  enabled: true
  port: 8080
```

### Run the Dashboard

```bash
# Start the dashboard (proxies API requests to the main process)
./bin/koptimizer-dash --api-url http://localhost:8080 --port 3000
# Open http://localhost:3000
```

### Run the MCP Server (for Claude Desktop / Claude Code)

```bash
./bin/koptimizer-mcp --api-url http://localhost:8080
```

Add to Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json`):
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

## Deploy with Helm

```bash
# Install with defaults (recommend mode)
helm install koptimizer deploy/helm/koptimizer \
  --namespace koptimizer --create-namespace \
  --set config.cloudProvider=aws \
  --set config.region=us-east-1

# Install with AI Safety Gate
helm install koptimizer deploy/helm/koptimizer \
  --namespace koptimizer --create-namespace \
  --set config.cloudProvider=aws \
  --set config.region=us-east-1 \
  --set config.mode=active \
  --set aiGateApiKey=sk-ant-xxxxx

# Using an existing K8s secret for the API key
helm install koptimizer deploy/helm/koptimizer \
  --namespace koptimizer --create-namespace \
  --set config.cloudProvider=aws \
  --set config.region=us-east-1 \
  --set aiGateApiKeySecretRef.name=my-anthropic-secret \
  --set aiGateApiKeySecretRef.key=ANTHROPIC_API_KEY
```

## Cloud Provider Setup

### AWS (EKS)
Requires IAM permissions for EC2, Auto Scaling, Pricing API, Cost Explorer, and Savings Plans. Use IRSA for production:
```yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/koptimizer-role
```

### GCP (GKE)
Requires `roles/compute.viewer`, `roles/compute.instanceAdmin.v1`, `roles/billing.viewer`. Use Workload Identity for production.

### Azure (AKS)
Requires `Reader`, `Virtual Machine Contributor`, `Cost Management Reader` on the resource group/subscription. Use Workload Identity Federation for production.

## Endpoints

| Port | Endpoint | Description |
|------|----------|-------------|
| 8080 | `/api/v1/*` | REST API (50+ endpoints) |
| 9090 | `/metrics` | Prometheus metrics |
| 8081 | `/healthz`, `/readyz` | Health probes |
| 3000 | `/` | Web dashboard |

### Key API Routes

```bash
# Cluster
curl localhost:8080/api/v1/cluster/summary
curl localhost:8080/api/v1/cluster/health

# Cost
curl localhost:8080/api/v1/cost/summary
curl localhost:8080/api/v1/cost/by-namespace
curl localhost:8080/api/v1/cost/savings

# Nodes & Node Groups
curl localhost:8080/api/v1/nodes
curl localhost:8080/api/v1/nodegroups

# Recommendations
curl localhost:8080/api/v1/recommendations
curl -X POST localhost:8080/api/v1/recommendations/{id}/approve

# Config
curl localhost:8080/api/v1/config
curl -X PUT localhost:8080/api/v1/config/mode -d '{"mode":"active"}'
```

## Project Structure

```
cmd/
  optimizer/     Main controller binary
  dashboard/     Web dashboard (SPA + reverse proxy)
  mcp/           MCP server for Claude integration
api/v1alpha1/    Kubernetes CRD types
internal/
  apiserver/     REST API handlers and router
  auth/          Authentication middleware (Azure AD + API keys)
  cloud/
    aws/         AWS EKS provider
    gcp/         GCP GKE provider
    azure/       Azure AKS provider
  config/        Configuration loading and validation
  controller/    Kubernetes controllers
    nodeautoscaler/  Node scaling up/down
    evictor/         Pod consolidation and node draining
    rightsizer/      Workload CPU/memory optimization
    workloadscaler/  Unified HPA+VPA
    rebalancer/      Scheduled workload redistribution
    spot/            Spot instance management
    gpu/             GPU optimization
    commitments/     RI/SP/CUD tracking
    hibernation/     Cluster hibernation
    costmonitor/     Cost attribution
    alerts/          Slack/webhook alerting
    storage/         PVC monitoring
    network/         Cross-AZ traffic cost
  state/         Cluster state cache
  store/         SQLite persistence (pricing cache, audit log)
  metrics/       Prometheus metrics and time-series store
pkg/
  aigate/        AI Safety Gate (Claude integration)
  cloudprovider/ Cloud provider interface
  familylock/    Family-Lock Guard
  cost/          Cost calculation constants
deploy/helm/     Helm chart
```

## Documentation

- **[Usage Guide](docs/USAGE.md)** — Full configuration reference, API docs, MCP tools, cloud setup, monitoring, safety guarantees, and troubleshooting
- **[Review Report](docs/REVIEW_REPORT.md)** — Code quality review with scores and fix history

## License

Proprietary. All rights reserved.
