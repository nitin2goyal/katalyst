package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var currentMode = "recommend"

func main() {
	port := flag.Int("port", 8080, "Mock API port")
	flag.Parse()

	mux := http.NewServeMux()

	// Existing endpoints
	mux.HandleFunc("/api/v1/cluster/summary", jsonHandler(clusterSummary))
	mux.HandleFunc("/api/v1/cluster/health", jsonHandler(clusterHealth))
	mux.HandleFunc("/api/v1/cluster/efficiency", jsonHandler(clusterEfficiency))
	mux.HandleFunc("/api/v1/cluster/score", jsonHandler(clusterScore))
	mux.HandleFunc("/api/v1/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, configHandler())
	})
	mux.HandleFunc("/api/v1/config/mode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if m, ok := body["mode"]; ok && (m == "recommend" || m == "enforce") {
				currentMode = m
			}
		}
		writeJSON(w, map[string]string{"status": "ok", "mode": currentMode})
	})
	mux.HandleFunc("/api/v1/nodegroups/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/nodegroups/")
		if path == "empty" {
			writeJSON(w, emptyNodeGroups())
			return
		}
		if strings.Contains(path, "/nodes") {
			id := strings.Split(path, "/")[0]
			writeJSON(w, nodeGroupNodesById(id))
			return
		}
		if path != "" {
			writeJSON(w, nodeGroupById(path))
			return
		}
		writeJSON(w, nodeGroups())
	})
	mux.HandleFunc("/api/v1/nodegroups", jsonHandler(nodeGroups))
	mux.HandleFunc("/api/v1/nodes/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/")
		if name != "" {
			writeJSON(w, nodeByName(name))
			return
		}
		writeJSON(w, nodeList())
	})
	mux.HandleFunc("/api/v1/nodes", jsonHandler(nodeList))
	mux.HandleFunc("/api/v1/cost/summary", jsonHandler(costSummary))
	mux.HandleFunc("/api/v1/cost/by-namespace", jsonHandler(costByNamespace))
	mux.HandleFunc("/api/v1/cost/by-workload", jsonHandler(costByWorkload))
	mux.HandleFunc("/api/v1/cost/trend", jsonHandler(costTrend))
	mux.HandleFunc("/api/v1/cost/savings", jsonHandler(costSavings))
	mux.HandleFunc("/api/v1/cost/by-label", jsonHandler(func() any { return map[string]any{} }))
	mux.HandleFunc("/api/v1/cost/network", jsonHandler(costNetwork))
	mux.HandleFunc("/api/v1/cost/comparison", jsonHandler(costComparison))
	mux.HandleFunc("/api/v1/recommendations/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/summary") {
			writeJSON(w, recSummary())
		} else if strings.Contains(path, "/approve") || strings.Contains(path, "/dismiss") {
			writeJSON(w, map[string]string{"status": "ok"})
		} else {
			writeJSON(w, recommendations())
		}
	})
	mux.HandleFunc("/api/v1/recommendations", jsonHandler(recommendations))
	mux.HandleFunc("/api/v1/workloads/efficiency", jsonHandler(workloadEfficiency))
	mux.HandleFunc("/api/v1/workloads/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/workloads/")
		if path == "efficiency" {
			writeJSON(w, workloadEfficiency())
			return
		}
		parts := strings.Split(path, "/")
		if len(parts) >= 3 {
			ns, kind, name := parts[0], parts[1], parts[2]
			if len(parts) == 4 && parts[3] == "rightsizing" {
				writeJSON(w, workloadRightsizing(ns, kind, name))
				return
			}
			if len(parts) == 4 && parts[3] == "scaling" {
				writeJSON(w, workloadScaling(ns, kind, name))
				return
			}
			writeJSON(w, workloadDetail(ns, kind, name))
			return
		}
		writeJSON(w, workloads())
	})
	mux.HandleFunc("/api/v1/workloads", jsonHandler(workloads))
	mux.HandleFunc("/api/v1/gpu/nodes", jsonHandler(gpuNodes))
	mux.HandleFunc("/api/v1/gpu/utilization", jsonHandler(gpuUtil))
	mux.HandleFunc("/api/v1/gpu/recommendations", jsonHandler(gpuRecs))
	mux.HandleFunc("/api/v1/commitments/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/underutilized") {
			writeJSON(w, underutilizedCommitments())
		} else if strings.HasSuffix(path, "/expiring") {
			writeJSON(w, expiringCommitments())
		} else {
			writeJSON(w, allCommitments())
		}
	})
	mux.HandleFunc("/api/v1/commitments", jsonHandler(allCommitments))
	mux.HandleFunc("/api/v1/audit", jsonHandler(auditEvents))
	mux.HandleFunc("/api/v1/notifications", jsonHandler(notificationsData))
	mux.HandleFunc("/api/v1/events", jsonHandler(eventsData))
	mux.HandleFunc("/api/v1/spot/summary", jsonHandler(spotSummary))
	mux.HandleFunc("/api/v1/spot/nodes", jsonHandler(spotNodes))
	mux.HandleFunc("/api/v1/clusters", jsonHandler(clustersData))
	mux.HandleFunc("/api/v1/metrics", metricsHandler)
	mux.HandleFunc("/api/v1/idle-resources", jsonHandler(idleResources))
	mux.HandleFunc("/api/v1/cost/impact", jsonHandler(costImpact))
	mux.HandleFunc("/api/v1/policies", jsonHandler(policiesData))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Mock KOptimizer API on %s", addr)
	log.Fatal(http.ListenAndServe(addr, corsMiddleware(mux)))
}

func jsonHandler(fn func() any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { writeJSON(w, fn()) }
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	json.NewEncoder(w).Encode(v)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jitter(base float64, pct float64) float64 {
	return base + base*pct*(rand.Float64()*2-1)
}

// ── Cluster ──
func clusterSummary() any {
	cm := computeClusterMetrics()
	return map[string]any{
		"mode":              currentMode,
		"cloudProvider":     "aws",
		"nodeCount":         cm.nodeCount,
		"podCount":          cm.totalPods,
		"nodeGroupCount":    len(nodeGroupData),
		"cpuUtilizationPct": jitter(cm.cpuUtilPct, 0.05),
		"memUtilizationPct": jitter(cm.memUtilPct, 0.05),
		"cpuAllocationPct":  jitter(cm.cpuAllocPct, 0.04),
		"memAllocationPct":  jitter(cm.memAllocPct, 0.04),
		"monthlyCostUSD":    jitter(cm.monthlyCostUSD, 0.02),
		"potentialSavings":  jitter(1834.20, 0.03),
	}
}

func clusterHealth() any {
	return map[string]any{
		"status": "healthy",
		"mode":   currentMode,
		"controllers": map[string]string{
			"costMonitor":    "healthy",
			"nodeAutoscaler": "healthy",
			"nodegroupMgr":   "healthy",
			"rightsizer":     "healthy",
			"workloadScaler": "healthy",
			"evictor":        "healthy",
			"rebalancer":     "healthy",
			"gpu":            "healthy",
			"commitments":    "healthy",
			"aiGate":         "disabled",
		},
	}
}

func clusterEfficiency() any {
	cm := computeClusterMetrics()

	// Savings score: spot ratio / 0.70 * 100 (same formula as real handler)
	savingsScore := cm.spotPct / 70 * 100
	if savingsScore > 100 {
		savingsScore = 100
	}
	// Commitment score: proxy from utilization
	commitScore := (cm.cpuUtilPct + cm.memUtilPct) / 2
	score := cm.cpuUtilPct*0.30 + cm.memUtilPct*0.30 + savingsScore*0.20 + commitScore*0.20

	return map[string]any{
		"score": math.Round(score),
		"breakdown": map[string]any{
			"cpu":         math.Round(cm.cpuUtilPct*10) / 10,
			"memory":      math.Round(cm.memUtilPct*10) / 10,
			"savings":     math.Round(savingsScore*10) / 10,
			"commitments": math.Round(commitScore*10) / 10,
		},
	}
}

func clusterScore() any {
	cm := computeClusterMetrics()

	// Count over-provisioned workloads from workload efficiency data.
	overProvCPU, overProvMem, rightsizable := 0, 0, 0
	for _, w := range workloadData {
		cpuReq := parseCPUMilli(w["cpuRequest"].(string))
		cpuUsed := parseCPUMilli(w["cpuUsed"].(string))
		memReq := parseMemMiB(w["memRequest"].(string))
		memUsed := parseMemMiB(w["memUsed"].(string))
		cpuOver := cpuReq > 0 && cpuUsed/cpuReq < 0.5
		memOver := memReq > 0 && memUsed/memReq < 0.5
		if cpuOver {
			overProvCPU++
		}
		if memOver {
			overProvMem++
		}
		if cpuOver && memOver {
			rightsizable++
		}
	}

	// ── 1. Provisioning ──
	provScore := 10.0
	var provFindings []string
	if cm.nodeCount > 0 {
		underutilRatio := float64(cm.underutilizedCount) / float64(cm.nodeCount)
		if cm.underutilizedCount > 0 {
			provScore -= underutilRatio * 4
			provFindings = append(provFindings,
				fmt.Sprintf("%d of %d nodes have CPU utilization below 50%%", cm.underutilizedCount, cm.nodeCount))
		}
		if cm.spotPct < 30 {
			provScore -= (30 - cm.spotPct) / 30 * 1.5
			provFindings = append(provFindings,
				fmt.Sprintf("Spot instances cover %.0f%% of nodes (target: 30%%+)", cm.spotPct))
		}
	}
	provFindings = append(provFindings, "Node group scaling policies are properly configured")
	provScore = clampMockScore(provScore)

	// ── 2. Workload Optimization ──
	wlScore := 10.0
	var wlFindings []string
	totalWl := len(workloadData)
	if totalWl > 0 {
		wlScore -= float64(overProvCPU) / float64(totalWl) * 3
		wlScore -= float64(overProvMem) / float64(totalWl) * 2
		if overProvCPU > 0 {
			wlFindings = append(wlFindings, fmt.Sprintf("%d workloads have CPU requests >2x actual usage", overProvCPU))
		}
		if overProvMem > 0 {
			wlFindings = append(wlFindings, fmt.Sprintf("%d workloads have memory requests >2x actual usage", overProvMem))
		}
		if rightsizable > 0 {
			// Identify specific workloads for the finding
			var names []string
			for _, w := range workloadData {
				cpuReq := parseCPUMilli(w["cpuRequest"].(string))
				cpuUsed := parseCPUMilli(w["cpuUsed"].(string))
				memReq := parseMemMiB(w["memRequest"].(string))
				memUsed := parseMemMiB(w["memUsed"].(string))
				if cpuReq > 0 && cpuUsed/cpuReq < 0.5 && memReq > 0 && memUsed/memReq < 0.5 {
					names = append(names, w["name"].(string))
				}
			}
			wlFindings = append(wlFindings, strings.Join(names, ", ")+" are candidates for rightsizing")
		}
	}
	wlScore = clampMockScore(wlScore)

	// ── 3. Cost Efficiency ──
	ceScore := 10.0
	var ceFindings []string
	avgUtil := (cm.cpuUtilPct + cm.memUtilPct) / 2
	if avgUtil < 70 {
		ceScore -= (70 - avgUtil) / 70 * 3
	}
	if cm.spotPct < 20 {
		ceScore -= (20 - cm.spotPct) / 20 * 2
	}
	ceFindings = append(ceFindings, "Reserved Instance utilization at 88% average")
	ceFindings = append(ceFindings, "Savings Plan utilization at only 42% — review coverage")
	ceFindings = append(ceFindings, "$1,834/mo in identified but uncaptured savings")
	ceScore = clampMockScore(ceScore)

	// ── 4. Resource Allocation ──
	raScore := 10.0
	var raFindings []string
	cpuGap := cm.cpuAllocPct - cm.cpuUtilPct
	memGap := cm.memAllocPct - cm.memUtilPct
	if cpuGap > 5 {
		raScore -= cpuGap / 30 * 4
		raFindings = append(raFindings,
			fmt.Sprintf("CPU allocation at %.0f%% but utilization only %.0f%% — %.0f%% over-provisioned",
				cm.cpuAllocPct, cm.cpuUtilPct, cpuGap))
	}
	if memGap > 5 {
		raScore -= memGap / 30 * 3
		raFindings = append(raFindings,
			fmt.Sprintf("Memory allocation at %.0f%% but utilization only %.0f%% — %.0f%% over-provisioned",
				cm.memAllocPct, cm.memUtilPct, memGap))
	}
	if cpuGap > 10 || memGap > 10 {
		estSavings := cm.monthlyCostUSD * ((cpuGap + memGap) / 2) / 100
		raFindings = append(raFindings,
			fmt.Sprintf("Tighter resource requests could free capacity and reduce ~$%.0f/mo in costs", estSavings))
	}
	raScore = clampMockScore(raScore)

	raDetails := "Resource allocation closely matches actual usage"
	if cpuGap > 10 || memGap > 10 {
		raDetails = "Significant gap between allocated resources and actual usage indicates over-provisioning"
	}

	overallScore := (provScore + wlScore + ceScore + raScore) / 4

	return map[string]any{
		"overallScore": math.Round(overallScore*10) / 10,
		"maxScore":     10.0,
		"categories": map[string]any{
			"provisioning": map[string]any{
				"score": math.Round(provScore*10) / 10, "maxScore": 10,
				"details":  "Node groups are well-sized with minor over-provisioning in memory-optimized tier",
				"findings": provFindings,
			},
			"workloadOptimization": map[string]any{
				"score": math.Round(wlScore*10) / 10, "maxScore": 10,
				"details":  "Several workloads have significant gaps between requests and actual usage",
				"findings": wlFindings,
			},
			"costEfficiency": map[string]any{
				"score": math.Round(ceScore*10) / 10, "maxScore": 10,
				"details":  "Good commitment coverage but savings plan utilization needs improvement",
				"findings": ceFindings,
			},
			"resourceAllocation": map[string]any{
				"score": math.Round(raScore*10) / 10, "maxScore": 10,
				"details":  raDetails,
				"findings": raFindings,
			},
		},
	}
}

func clampMockScore(s float64) float64 {
	if s < 0 {
		return 0
	}
	if s > 10 {
		return 10
	}
	return s
}

func configHandler() any {
	return map[string]any{
		"mode":          currentMode,
		"cloudProvider":  "aws",
		"region":        "us-east-1",
		"clusterName":   "demo-cluster",
		"controllers": map[string]bool{
			"costMonitor": true, "nodeAutoscaler": true, "nodegroupMgr": true,
			"rightsizer": true, "workloadScaler": true, "evictor": true,
			"rebalancer": true, "gpu": true, "commitments": true, "aiGate": false,
		},
	}
}

// ── Node Groups ──
var nodeGroupData = []map[string]any{
	{"id": "ng-general-1", "name": "general-purpose", "instanceType": "m5.xlarge", "instanceFamily": "m5", "currentCount": 4, "minCount": 2, "maxCount": 8, "desiredCount": 4, "cpuUtilPct": 58.0, "memUtilPct": 72.0, "totalPods": 18, "monthlyCostUSD": 2803.0, "isEmpty": false},
	{"id": "ng-compute-1", "name": "compute-optimized", "instanceType": "c5.2xlarge", "instanceFamily": "c5", "currentCount": 3, "minCount": 1, "maxCount": 6, "desiredCount": 3, "cpuUtilPct": 78.0, "memUtilPct": 55.0, "totalPods": 14, "monthlyCostUSD": 2978.0, "isEmpty": false},
	{"id": "ng-memory-1", "name": "memory-optimized", "instanceType": "r5.xlarge", "instanceFamily": "r5", "currentCount": 3, "minCount": 1, "maxCount": 5, "desiredCount": 3, "cpuUtilPct": 42.0, "memUtilPct": 81.0, "totalPods": 10, "monthlyCostUSD": 2199.0, "isEmpty": false},
	{"id": "ng-spot-1", "name": "spot-workers", "instanceType": "m5.large", "instanceFamily": "m5", "currentCount": 2, "minCount": 0, "maxCount": 10, "desiredCount": 2, "cpuUtilPct": 85.0, "memUtilPct": 67.0, "totalPods": 5, "monthlyCostUSD": 267.0, "isEmpty": false},
}

func nodeGroups() any {
	result := make([]map[string]any, len(nodeGroupData))
	for i, ng := range nodeGroupData {
		cp := make(map[string]any)
		for k, v := range ng {
			cp[k] = v
		}
		if f, ok := cp["cpuUtilPct"].(float64); ok {
			cp["cpuUtilPct"] = jitter(f, 0.1)
		}
		if f, ok := cp["memUtilPct"].(float64); ok {
			cp["memUtilPct"] = jitter(f, 0.08)
		}
		if f, ok := cp["monthlyCostUSD"].(float64); ok {
			cp["monthlyCostUSD"] = jitter(f, 0.02)
		}
		result[i] = cp
	}
	return result
}

func nodeGroupById(id string) any {
	for _, ng := range nodeGroupData {
		if ng["id"] == id || ng["name"] == id {
			cp := make(map[string]any)
			for k, v := range ng {
				cp[k] = v
			}
			return cp
		}
	}
	return map[string]any{"error": "not found"}
}

func nodeGroupNodesById(id string) any {
	allNodes := nodeListData()
	var result []map[string]any
	for _, n := range allNodes {
		ngName := ""
		for _, ng := range nodeGroupData {
			if ng["id"] == id || ng["name"] == id {
				ngName, _ = ng["name"].(string)
				break
			}
		}
		if n["nodeGroup"] == ngName || n["nodeGroup"] == id {
			result = append(result, n)
		}
	}
	if result == nil {
		result = []map[string]any{}
	}
	return result
}

func emptyNodeGroups() any { return []map[string]any{} }

// ── Nodes ──
func nodeListData() []map[string]any {
	return []map[string]any{
		{"name": "ip-10-0-1-101.ec2", "instanceType": "m5.xlarge", "instanceFamily": "m5", "nodeGroup": "general-purpose", "nodeGroupId": "ng-general-1", "cpuCapacity": "4000m", "memCapacity": "16Gi", "cpuUsed": "2320m", "memUsed": "11.5Gi", "cpuRequested": "3000m", "memRequested": "14.0Gi", "cpuUtilPct": 58.0, "memUtilPct": 71.9, "hourlyCostUSD": 0.192, "isSpot": false, "isGPU": false, "podCount": 5, "az": "us-east-1a", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 100, "iops": 3000, "throughputMBps": 125, "encrypted": true}, {"name": "/dev/xvdb", "type": "gp3", "sizeGiB": 200, "iops": 3000, "throughputMBps": 125, "encrypted": true}}},
		{"name": "ip-10-0-1-102.ec2", "instanceType": "m5.xlarge", "instanceFamily": "m5", "nodeGroup": "general-purpose", "nodeGroupId": "ng-general-1", "cpuCapacity": "4000m", "memCapacity": "16Gi", "cpuUsed": "2680m", "memUsed": "12.1Gi", "cpuRequested": "3200m", "memRequested": "14.5Gi", "cpuUtilPct": 67.0, "memUtilPct": 75.6, "hourlyCostUSD": 0.192, "isSpot": false, "isGPU": false, "podCount": 6, "az": "us-east-1a", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 100, "iops": 3000, "throughputMBps": 125, "encrypted": true}, {"name": "/dev/xvdb", "type": "gp3", "sizeGiB": 200, "iops": 3000, "throughputMBps": 125, "encrypted": true}}},
		{"name": "ip-10-0-2-101.ec2", "instanceType": "m5.xlarge", "instanceFamily": "m5", "nodeGroup": "general-purpose", "nodeGroupId": "ng-general-1", "cpuCapacity": "4000m", "memCapacity": "16Gi", "cpuUsed": "1960m", "memUsed": "10.8Gi", "cpuRequested": "2600m", "memRequested": "13.0Gi", "cpuUtilPct": 49.0, "memUtilPct": 67.5, "hourlyCostUSD": 0.192, "isSpot": false, "isGPU": false, "podCount": 4, "az": "us-east-1b", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 100, "iops": 3000, "throughputMBps": 125, "encrypted": true}}},
		{"name": "ip-10-0-2-102.ec2", "instanceType": "m5.xlarge", "instanceFamily": "m5", "nodeGroup": "general-purpose", "nodeGroupId": "ng-general-1", "cpuCapacity": "4000m", "memCapacity": "16Gi", "cpuUsed": "2440m", "memUsed": "12.3Gi", "cpuRequested": "3100m", "memRequested": "14.8Gi", "cpuUtilPct": 61.0, "memUtilPct": 76.9, "hourlyCostUSD": 0.192, "isSpot": false, "isGPU": false, "podCount": 3, "az": "us-east-1b", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 100, "iops": 3000, "throughputMBps": 125, "encrypted": true}}},
		{"name": "ip-10-0-3-101.ec2", "instanceType": "c5.2xlarge", "instanceFamily": "c5", "nodeGroup": "compute-optimized", "nodeGroupId": "ng-compute-1", "cpuCapacity": "8000m", "memCapacity": "16Gi", "cpuUsed": "6560m", "memUsed": "9.2Gi", "cpuRequested": "7200m", "memRequested": "12.0Gi", "cpuUtilPct": 82.0, "memUtilPct": 57.5, "hourlyCostUSD": 0.34, "isSpot": false, "isGPU": false, "podCount": 5, "az": "us-east-1a", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 50, "iops": 3000, "throughputMBps": 125, "encrypted": true}, {"name": "/dev/xvdb", "type": "io2", "sizeGiB": 500, "iops": 16000, "throughputMBps": 500, "encrypted": true}}},
		{"name": "ip-10-0-3-102.ec2", "instanceType": "c5.2xlarge", "instanceFamily": "c5", "nodeGroup": "compute-optimized", "nodeGroupId": "ng-compute-1", "cpuCapacity": "8000m", "memCapacity": "16Gi", "cpuUsed": "5840m", "memUsed": "8.4Gi", "cpuRequested": "6400m", "memRequested": "11.0Gi", "cpuUtilPct": 73.0, "memUtilPct": 52.5, "hourlyCostUSD": 0.34, "isSpot": false, "isGPU": false, "podCount": 5, "az": "us-east-1b", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 50, "iops": 3000, "throughputMBps": 125, "encrypted": true}, {"name": "/dev/xvdb", "type": "io2", "sizeGiB": 500, "iops": 16000, "throughputMBps": 500, "encrypted": true}}},
		{"name": "ip-10-0-3-103.ec2", "instanceType": "c5.2xlarge", "instanceFamily": "c5", "nodeGroup": "compute-optimized", "nodeGroupId": "ng-compute-1", "cpuCapacity": "8000m", "memCapacity": "16Gi", "cpuUsed": "6320m", "memUsed": "8.8Gi", "cpuRequested": "7000m", "memRequested": "11.5Gi", "cpuUtilPct": 79.0, "memUtilPct": 55.0, "hourlyCostUSD": 0.34, "isSpot": false, "isGPU": false, "podCount": 4, "az": "us-east-1c", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 50, "iops": 3000, "throughputMBps": 125, "encrypted": true}, {"name": "/dev/xvdb", "type": "io2", "sizeGiB": 500, "iops": 16000, "throughputMBps": 500, "encrypted": true}}},
		{"name": "ip-10-0-4-101.ec2", "instanceType": "r5.xlarge", "instanceFamily": "r5", "nodeGroup": "memory-optimized", "nodeGroupId": "ng-memory-1", "cpuCapacity": "4000m", "memCapacity": "32Gi", "cpuUsed": "1680m", "memUsed": "26.2Gi", "cpuRequested": "2200m", "memRequested": "29.0Gi", "cpuUtilPct": 42.0, "memUtilPct": 81.9, "hourlyCostUSD": 0.252, "isSpot": false, "isGPU": false, "podCount": 4, "az": "us-east-1a", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 100, "iops": 3000, "throughputMBps": 125, "encrypted": true}, {"name": "/dev/xvdb", "type": "gp3", "sizeGiB": 1000, "iops": 6000, "throughputMBps": 250, "encrypted": true}}},
		{"name": "ip-10-0-4-102.ec2", "instanceType": "r5.xlarge", "instanceFamily": "r5", "nodeGroup": "memory-optimized", "nodeGroupId": "ng-memory-1", "cpuCapacity": "4000m", "memCapacity": "32Gi", "cpuUsed": "1520m", "memUsed": "25.8Gi", "cpuRequested": "2000m", "memRequested": "28.5Gi", "cpuUtilPct": 38.0, "memUtilPct": 80.6, "hourlyCostUSD": 0.252, "isSpot": false, "isGPU": false, "podCount": 3, "az": "us-east-1b", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 100, "iops": 3000, "throughputMBps": 125, "encrypted": true}, {"name": "/dev/xvdb", "type": "gp3", "sizeGiB": 1000, "iops": 6000, "throughputMBps": 250, "encrypted": true}}},
		{"name": "ip-10-0-4-103.ec2", "instanceType": "r5.xlarge", "instanceFamily": "r5", "nodeGroup": "memory-optimized", "nodeGroupId": "ng-memory-1", "cpuCapacity": "4000m", "memCapacity": "32Gi", "cpuUsed": "1800m", "memUsed": "25.3Gi", "cpuRequested": "2400m", "memRequested": "28.0Gi", "cpuUtilPct": 45.0, "memUtilPct": 79.1, "hourlyCostUSD": 0.252, "isSpot": false, "isGPU": false, "podCount": 3, "az": "us-east-1c", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp3", "sizeGiB": 100, "iops": 3000, "throughputMBps": 125, "encrypted": true}, {"name": "/dev/xvdb", "type": "gp3", "sizeGiB": 1000, "iops": 6000, "throughputMBps": 250, "encrypted": true}}},
		{"name": "ip-10-0-5-101.ec2", "instanceType": "m5.large", "instanceFamily": "m5", "nodeGroup": "spot-workers", "nodeGroupId": "ng-spot-1", "cpuCapacity": "2000m", "memCapacity": "8Gi", "cpuUsed": "1720m", "memUsed": "5.4Gi", "cpuRequested": "1850m", "memRequested": "6.5Gi", "cpuUtilPct": 86.0, "memUtilPct": 67.5, "hourlyCostUSD": 0.031, "isSpot": true, "isGPU": false, "podCount": 3, "az": "us-east-1a", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp2", "sizeGiB": 50, "iops": 150, "throughputMBps": 128, "encrypted": false}}},
		{"name": "ip-10-0-5-102.ec2", "instanceType": "m5.large", "instanceFamily": "m5", "nodeGroup": "spot-workers", "nodeGroupId": "ng-spot-1", "cpuCapacity": "2000m", "memCapacity": "8Gi", "cpuUsed": "1680m", "memUsed": "5.3Gi", "cpuRequested": "1800m", "memRequested": "6.3Gi", "cpuUtilPct": 84.0, "memUtilPct": 66.3, "hourlyCostUSD": 0.031, "isSpot": true, "isGPU": false, "podCount": 2, "az": "us-east-1b", "disks": []map[string]any{{"name": "/dev/xvda", "type": "gp2", "sizeGiB": 50, "iops": 150, "throughputMBps": 128, "encrypted": false}}},
	}
}

// parseCPUMilli parses CPU strings like "4000m" to millicores.
func parseCPUMilli(s string) float64 {
	s = strings.TrimSuffix(s, "m")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// parseMemMiB parses memory strings like "16Gi" or "128Mi" to MiB.
func parseMemMiB(s string) float64 {
	if strings.HasSuffix(s, "Gi") {
		s = strings.TrimSuffix(s, "Gi")
		v, _ := strconv.ParseFloat(s, 64)
		return v * 1024
	}
	s = strings.TrimSuffix(s, "Mi")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// clusterMetrics aggregates node data to derive cluster-wide metrics.
type clusterMetrics struct {
	cpuUtilPct, memUtilPct     float64
	cpuAllocPct, memAllocPct   float64
	spotPct                    float64
	monthlyCostUSD             float64
	nodeCount, spotCount       int
	underutilizedCount         int
	totalPods                  int
}

func computeClusterMetrics() clusterMetrics {
	nodes := nodeListData()
	var totalCPU, totalMem, usedCPU, usedMem, reqCPU, reqMem float64
	var spotCount, underutilCount, totalPods int

	for _, n := range nodes {
		capCPU := parseCPUMilli(n["cpuCapacity"].(string))
		capMem := parseMemMiB(n["memCapacity"].(string))
		uCPU := parseCPUMilli(n["cpuUsed"].(string))
		uMem := parseMemMiB(n["memUsed"].(string))
		rCPU := parseCPUMilli(n["cpuRequested"].(string))
		rMem := parseMemMiB(n["memRequested"].(string))

		totalCPU += capCPU
		totalMem += capMem
		usedCPU += uCPU
		usedMem += uMem
		reqCPU += rCPU
		reqMem += rMem

		if n["isSpot"].(bool) {
			spotCount++
		}
		if n["cpuUtilPct"].(float64) < 50 && n["memUtilPct"].(float64) < 50 {
			underutilCount++
		}
		totalPods += n["podCount"].(int)
	}

	cm := clusterMetrics{
		nodeCount:          len(nodes),
		spotCount:          spotCount,
		underutilizedCount: underutilCount,
		totalPods:          totalPods,
	}
	if totalCPU > 0 {
		cm.cpuUtilPct = usedCPU / totalCPU * 100
		cm.cpuAllocPct = reqCPU / totalCPU * 100
	}
	if totalMem > 0 {
		cm.memUtilPct = usedMem / totalMem * 100
		cm.memAllocPct = reqMem / totalMem * 100
	}
	if len(nodes) > 0 {
		cm.spotPct = float64(spotCount) / float64(len(nodes)) * 100
	}
	var totalHourly float64
	for _, n := range nodes {
		totalHourly += n["hourlyCostUSD"].(float64)
	}
	cm.monthlyCostUSD = totalHourly * 730
	return cm
}

func nodeList() any {
	nodes := nodeListData()
	for i := range nodes {
		nodes[i]["cpuUtilPct"] = jitter(nodes[i]["cpuUtilPct"].(float64), 0.03)
		nodes[i]["memUtilPct"] = jitter(nodes[i]["memUtilPct"].(float64), 0.03)
	}
	return nodes
}

func nodeByName(name string) any {
	for _, n := range nodeListData() {
		if n["name"] == name {
			pods := podsByNode(name)
			return map[string]any{
				"node": n,
				"pods": pods,
			}
		}
	}
	return map[string]any{"error": "not found"}
}

func podsByNode(nodeName string) []map[string]any {
	podData := map[string][]map[string]any{
		"ip-10-0-1-101.ec2": {
			{"name": "web-frontend-6d4f7b8c9-x7k2m", "namespace": "production", "cpuRequest": "100m", "memRequest": "128Mi", "status": "Running"},
			{"name": "api-server-5c8d9e7f6-p3n1q", "namespace": "production", "cpuRequest": "250m", "memRequest": "256Mi", "status": "Running"},
			{"name": "kube-proxy-abc12", "namespace": "kube-system", "cpuRequest": "100m", "memRequest": "128Mi", "status": "Running"},
			{"name": "coredns-7d8f9c6b5-m2k4j", "namespace": "kube-system", "cpuRequest": "100m", "memRequest": "70Mi", "status": "Running"},
			{"name": "cache-8e5f4d3c2-r9s7t", "namespace": "production", "cpuRequest": "100m", "memRequest": "256Mi", "status": "Running"},
		},
		"ip-10-0-1-102.ec2": {
			{"name": "web-frontend-6d4f7b8c9-a2b3c", "namespace": "production", "cpuRequest": "100m", "memRequest": "128Mi", "status": "Running"},
			{"name": "api-server-5c8d9e7f6-d4e5f", "namespace": "production", "cpuRequest": "250m", "memRequest": "256Mi", "status": "Running"},
			{"name": "worker-9f6e5d4c3-g6h7i", "namespace": "production", "cpuRequest": "200m", "memRequest": "512Mi", "status": "Running"},
			{"name": "worker-9f6e5d4c3-j8k9l", "namespace": "production", "cpuRequest": "200m", "memRequest": "512Mi", "status": "Running"},
			{"name": "kube-proxy-def34", "namespace": "kube-system", "cpuRequest": "100m", "memRequest": "128Mi", "status": "Running"},
			{"name": "grafana-4b3a2c1d0-n1o2p", "namespace": "monitoring", "cpuRequest": "100m", "memRequest": "128Mi", "status": "Running"},
		},
	}
	if pods, ok := podData[nodeName]; ok {
		return pods
	}
	return []map[string]any{
		{"name": "kube-proxy-" + nodeName[:8], "namespace": "kube-system", "cpuRequest": "100m", "memRequest": "128Mi", "status": "Running"},
		{"name": "app-pod-1", "namespace": "production", "cpuRequest": "200m", "memRequest": "256Mi", "status": "Running"},
		{"name": "app-pod-2", "namespace": "production", "cpuRequest": "150m", "memRequest": "192Mi", "status": "Running"},
	}
}

// ── Cost ──
func costSummary() any {
	return map[string]any{
		"totalMonthlyCostUSD":     jitter(8247.50, 0.02),
		"projectedMonthlyCostUSD": jitter(8890.00, 0.02),
		"nodeCount":               12,
		"potentialSavings":        jitter(1834.20, 0.03),
	}
}

func costByNamespace() any {
	return map[string]any{
		"production":  jitter(5120.30, 0.03),
		"staging":     jitter(892.40, 0.05),
		"monitoring":  jitter(1347.60, 0.04),
		"kube-system": jitter(487.20, 0.02),
		"default":     jitter(400.00, 0.05),
	}
}

func costByWorkload() any {
	return []map[string]any{
		{"namespace": "production", "kind": "Deployment", "name": "web-frontend", "monthlyCostUSD": jitter(1240.50, 0.03)},
		{"namespace": "production", "kind": "Deployment", "name": "api-server", "monthlyCostUSD": jitter(1680.20, 0.03)},
		{"namespace": "production", "kind": "Deployment", "name": "worker", "monthlyCostUSD": jitter(1450.80, 0.03)},
		{"namespace": "production", "kind": "Deployment", "name": "cache", "monthlyCostUSD": jitter(748.80, 0.04)},
		{"namespace": "staging", "kind": "Deployment", "name": "staging-app", "monthlyCostUSD": jitter(892.40, 0.05)},
		{"namespace": "monitoring", "kind": "Deployment", "name": "prometheus", "monthlyCostUSD": jitter(958.30, 0.03)},
		{"namespace": "monitoring", "kind": "Deployment", "name": "grafana", "monthlyCostUSD": jitter(389.30, 0.04)},
		{"namespace": "kube-system", "kind": "DaemonSet", "name": "kube-proxy", "monthlyCostUSD": jitter(312.40, 0.02)},
		{"namespace": "kube-system", "kind": "Deployment", "name": "coredns", "monthlyCostUSD": jitter(174.80, 0.03)},
	}
}

func costTrend() any {
	now := time.Now()
	points := make([]map[string]any, 30)
	base := 270.0
	for i := 0; i < 30; i++ {
		day := now.AddDate(0, 0, -29+i)
		weekdayMod := 1.0
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			weekdayMod = 0.7
		}
		cost := (base + float64(i)*1.5) * weekdayMod * (1 + 0.08*(rand.Float64()*2-1))
		points[i] = map[string]any{
			"date": day.Format("2006-01-02"),
			"cost": math.Round(cost*100) / 100,
		}
	}
	return map[string]any{"dataPoints": points}
}

func costSavings() any {
	return map[string]any{
		"opportunities": []map[string]any{
			{"type": "rightsizing", "name": "Downsize r5.xlarge nodes", "description": "memory-optimized group CPU util is only 42%. Switch to r5.large to save ~$732/mo", "estimatedSavings": 732.00},
			{"type": "spot", "name": "Move staging to spot", "description": "staging-app is non-critical. Using spot instances saves ~$580/mo", "estimatedSavings": 580.00},
			{"type": "commitment", "name": "Reserved instances for production", "description": "general-purpose and compute-optimized run 24/7. RIs save ~$522/mo", "estimatedSavings": 522.20},
		},
	}
}

func costNetwork() any {
	return map[string]any{
		"totalMonthlyCostUSD": 342.80,
		"crossAZCostUSD":      278.40,
		"inAZCostUSD":         64.40,
		"flows": []map[string]any{
			{"namespace": "production", "workload": "api-server", "sourceAZ": "us-east-1a", "destAZ": "us-east-1b", "trafficGB": 45.2, "monthlyCostUSD": 90.40},
			{"namespace": "production", "workload": "worker", "sourceAZ": "us-east-1a", "destAZ": "us-east-1c", "trafficGB": 38.6, "monthlyCostUSD": 77.20},
			{"namespace": "production", "workload": "web-frontend", "sourceAZ": "us-east-1b", "destAZ": "us-east-1a", "trafficGB": 28.4, "monthlyCostUSD": 56.80},
			{"namespace": "monitoring", "workload": "prometheus", "sourceAZ": "us-east-1a", "destAZ": "us-east-1b", "trafficGB": 18.0, "monthlyCostUSD": 36.00},
			{"namespace": "monitoring", "workload": "prometheus", "sourceAZ": "us-east-1a", "destAZ": "us-east-1c", "trafficGB": 9.0, "monthlyCostUSD": 18.00},
			{"namespace": "production", "workload": "cache", "sourceAZ": "us-east-1a", "destAZ": "us-east-1a", "trafficGB": 22.0, "monthlyCostUSD": 0.0},
			{"namespace": "production", "workload": "api-server", "sourceAZ": "us-east-1a", "destAZ": "us-east-1a", "trafficGB": 32.2, "monthlyCostUSD": 0.0},
			{"namespace": "kube-system", "workload": "coredns", "sourceAZ": "us-east-1a", "destAZ": "us-east-1a", "trafficGB": 12.8, "monthlyCostUSD": 0.0},
			{"namespace": "staging", "workload": "staging-app", "sourceAZ": "us-east-1b", "destAZ": "us-east-1b", "trafficGB": 5.4, "monthlyCostUSD": 0.0},
			{"namespace": "default", "workload": "koptimizer", "sourceAZ": "us-east-1a", "destAZ": "us-east-1a", "trafficGB": 1.2, "monthlyCostUSD": 0.0},
		},
	}
}

func costComparison() any {
	now := time.Now()
	thisMonth := now.Format("2006-01")
	lastMonth := now.AddDate(0, -1, 0).Format("2006-01")
	return map[string]any{
		"currentPeriod": thisMonth,
		"previousPeriod": lastMonth,
		"current": map[string]any{
			"totalCost":   8247.50,
			"computeCost": 6180.30,
			"storageCost": 1240.20,
			"networkCost": 342.80,
			"otherCost":   484.20,
		},
		"previous": map[string]any{
			"totalCost":   9120.80,
			"computeCost": 6890.40,
			"storageCost": 1280.40,
			"networkCost": 398.60,
			"otherCost":   551.40,
		},
		"byNamespace": []map[string]any{
			{"namespace": "production", "currentCost": 5120.30, "previousCost": 5680.50, "change": -9.86},
			{"namespace": "staging", "currentCost": 892.40, "previousCost": 1120.30, "change": -20.34},
			{"namespace": "monitoring", "currentCost": 1347.60, "previousCost": 1380.20, "change": -2.36},
			{"namespace": "kube-system", "currentCost": 487.20, "previousCost": 512.80, "change": -4.99},
			{"namespace": "default", "currentCost": 400.00, "previousCost": 427.00, "change": -6.32},
		},
	}
}

// ── Recommendations ──
func recommendations() any {
	return []map[string]any{
		{"id": "rec-001", "type": "rightsizing", "target": "memory-optimized (r5.xlarge)", "description": "CPU utilization averaging 42%. Recommend downsizing to r5.large", "estimatedSavings": 732.00, "status": "pending", "priority": "high", "createdAt": "2026-02-19T10:30:00Z", "confidence": 92, "dataPointsDays": 14},
		{"id": "rec-002", "type": "spot-migration", "target": "staging/staging-app", "description": "Non-critical workload suitable for spot instances", "estimatedSavings": 580.00, "status": "pending", "priority": "medium", "createdAt": "2026-02-19T11:00:00Z", "confidence": 87, "dataPointsDays": 30},
		{"id": "rec-003", "type": "consolidation", "target": "general-purpose node ip-10-0-2-101", "description": "Node at 49% CPU. Pods can be redistributed to other nodes", "estimatedSavings": 138.24, "status": "pending", "priority": "medium", "createdAt": "2026-02-20T08:15:00Z", "confidence": 78, "dataPointsDays": 7},
		{"id": "rec-004", "type": "rightsizing", "target": "production/web-frontend", "description": "Container CPU limit 200m but only using 80m avg. Reduce to 120m", "estimatedSavings": 45.60, "status": "approved", "priority": "low", "createdAt": "2026-02-18T14:20:00Z", "confidence": 95, "dataPointsDays": 21},
		{"id": "rec-005", "type": "commitment", "target": "general-purpose + compute-optimized", "description": "Stable on-demand usage. 1yr reserved instances save 38%", "estimatedSavings": 522.20, "status": "pending", "priority": "high", "createdAt": "2026-02-17T09:00:00Z", "confidence": 85, "dataPointsDays": 60},
		{"id": "rec-006", "type": "scaling", "target": "production/worker", "description": "Worker HPA hitting max replicas during peak. Increase max from 4 to 6", "estimatedSavings": 0, "status": "approved", "priority": "high", "createdAt": "2026-02-20T06:45:00Z", "confidence": 91, "dataPointsDays": 14},
		{"id": "rec-007", "type": "rightsizing", "target": "monitoring/prometheus", "description": "Memory request 512Mi but using 320Mi avg. Reduce to 384Mi", "estimatedSavings": 28.40, "status": "dismissed", "priority": "low", "createdAt": "2026-02-16T16:30:00Z", "confidence": 72, "dataPointsDays": 7},
	}
}

func recSummary() any {
	return map[string]any{
		"total": 7, "pending": 4, "approved": 2, "dismissed": 1,
		"totalEstimatedSavings": 2046.44,
	}
}

// ── Workloads ──
var workloadData = []map[string]any{
	{"namespace": "production", "kind": "Deployment", "name": "web-frontend", "replicas": 3, "totalCPU": "300m", "totalMem": "384Mi", "cpuRequest": "100m", "memRequest": "128Mi", "cpuLimit": "200m", "memLimit": "256Mi", "cpuUsed": "80m", "memUsed": "95Mi"},
	{"namespace": "production", "kind": "Deployment", "name": "api-server", "replicas": 2, "totalCPU": "500m", "totalMem": "512Mi", "cpuRequest": "250m", "memRequest": "256Mi", "cpuLimit": "500m", "memLimit": "512Mi", "cpuUsed": "180m", "memUsed": "210Mi"},
	{"namespace": "production", "kind": "Deployment", "name": "worker", "replicas": 4, "totalCPU": "800m", "totalMem": "2Gi", "cpuRequest": "200m", "memRequest": "512Mi", "cpuLimit": "400m", "memLimit": "1Gi", "cpuUsed": "350m", "memUsed": "480Mi"},
	{"namespace": "production", "kind": "Deployment", "name": "cache", "replicas": 2, "totalCPU": "200m", "totalMem": "512Mi", "cpuRequest": "100m", "memRequest": "256Mi", "cpuLimit": "200m", "memLimit": "512Mi", "cpuUsed": "60m", "memUsed": "200Mi"},
	{"namespace": "staging", "kind": "Deployment", "name": "staging-app", "replicas": 2, "totalCPU": "200m", "totalMem": "256Mi", "cpuRequest": "100m", "memRequest": "128Mi", "cpuLimit": "200m", "memLimit": "256Mi", "cpuUsed": "45m", "memUsed": "78Mi"},
	{"namespace": "monitoring", "kind": "Deployment", "name": "prometheus", "replicas": 1, "totalCPU": "200m", "totalMem": "512Mi", "cpuRequest": "200m", "memRequest": "512Mi", "cpuLimit": "500m", "memLimit": "1Gi", "cpuUsed": "150m", "memUsed": "320Mi"},
	{"namespace": "monitoring", "kind": "Deployment", "name": "grafana", "replicas": 1, "totalCPU": "100m", "totalMem": "128Mi", "cpuRequest": "100m", "memRequest": "128Mi", "cpuLimit": "200m", "memLimit": "256Mi", "cpuUsed": "55m", "memUsed": "90Mi"},
	{"namespace": "kube-system", "kind": "DaemonSet", "name": "kube-proxy", "replicas": 12, "totalCPU": "1200m", "totalMem": "1536Mi", "cpuRequest": "100m", "memRequest": "128Mi", "cpuLimit": "200m", "memLimit": "256Mi", "cpuUsed": "30m", "memUsed": "45Mi"},
	{"namespace": "kube-system", "kind": "Deployment", "name": "coredns", "replicas": 2, "totalCPU": "200m", "totalMem": "140Mi", "cpuRequest": "100m", "memRequest": "70Mi", "cpuLimit": "200m", "memLimit": "170Mi", "cpuUsed": "20m", "memUsed": "35Mi"},
	{"namespace": "default", "kind": "Deployment", "name": "koptimizer", "replicas": 1, "totalCPU": "100m", "totalMem": "128Mi", "cpuRequest": "100m", "memRequest": "128Mi", "cpuLimit": "200m", "memLimit": "256Mi", "cpuUsed": "40m", "memUsed": "75Mi"},
	{"namespace": "default", "kind": "Deployment", "name": "koptimizer-dashboard", "replicas": 1, "totalCPU": "50m", "totalMem": "64Mi", "cpuRequest": "50m", "memRequest": "64Mi", "cpuLimit": "100m", "memLimit": "128Mi", "cpuUsed": "10m", "memUsed": "25Mi"},
}

func workloads() any {
	return workloadData
}

func workloadDetail(ns, kind, name string) any {
	for _, w := range workloadData {
		if w["namespace"] == ns && w["kind"] == kind && w["name"] == name {
			return map[string]any{"workload": w}
		}
	}
	return map[string]any{"workload": map[string]any{
		"namespace": ns, "kind": kind, "name": name,
		"replicas": 1, "totalCPU": "100m", "totalMem": "128Mi",
	}}
}

func workloadRightsizing(ns, kind, name string) any {
	for _, w := range workloadData {
		if w["namespace"] == ns && w["kind"] == kind && w["name"] == name {
			return map[string]any{
				"current": map[string]any{
					"cpuRequest": w["cpuRequest"],
					"cpuLimit":   w["cpuLimit"],
					"memRequest": w["memRequest"],
					"memLimit":   w["memLimit"],
				},
				"recommended": map[string]any{
					"cpuRequest": w["cpuUsed"],
					"cpuLimit":   w["cpuRequest"],
					"memRequest": w["memUsed"],
					"memLimit":   w["memRequest"],
				},
				"estimatedSavings": jitter(45.0, 0.2),
				"reason":           fmt.Sprintf("Based on 7-day P95 usage analysis, %s can safely reduce resource requests. CPU P95 is %s vs current request of %s.", name, w["cpuUsed"], w["cpuRequest"]),
			}
		}
	}
	return map[string]any{}
}

func workloadScaling(ns, kind, name string) any {
	hasHPA := name == "worker" || name == "web-frontend" || name == "api-server"
	for _, w := range workloadData {
		if w["namespace"] == ns && w["kind"] == kind && w["name"] == name {
			replicas, _ := w["replicas"].(int)
			result := map[string]any{
				"currentReplicas": replicas,
				"minReplicas":     1,
				"maxReplicas":     replicas * 2,
				"replicas":        true,
			}
			if hasHPA {
				result["hpa"] = map[string]any{
					"enabled":           true,
					"minReplicas":       max(1, replicas/2),
					"maxReplicas":       replicas * 2,
					"targetCPUPct":      70.0,
					"currentCPUPct":     jitter(55.0, 0.15),
					"scaleUpCooldown":   "3m",
					"scaleDownCooldown": "5m",
				}
			} else {
				result["hpa"] = map[string]any{"enabled": false}
			}
			return result
		}
	}
	return map[string]any{}
}

func workloadEfficiency() any {
	type wlEff struct {
		ns, kind, name string
		cpuReq, cpuUsed, memReq, memUsed string
		cpuEffPct, memEffPct float64
		wastedCPU, wastedMem string
		monthlyCostUSD, wastedCostUSD float64
	}
	effData := []wlEff{
		{"production", "Deployment", "web-frontend", "100m", "80m", "128Mi", "95Mi", 80.0, 74.2, "20m", "33Mi", 413.50, 82.70},
		{"production", "Deployment", "api-server", "250m", "180m", "256Mi", "210Mi", 72.0, 82.0, "70m", "46Mi", 840.10, 151.22},
		{"production", "Deployment", "worker", "200m", "350m", "512Mi", "480Mi", 100.0, 93.8, "0m", "32Mi", 362.70, 22.67},
		{"production", "Deployment", "cache", "100m", "60m", "256Mi", "200Mi", 60.0, 78.1, "40m", "56Mi", 374.40, 82.37},
		{"staging", "Deployment", "staging-app", "100m", "45m", "128Mi", "78Mi", 45.0, 60.9, "55m", "50Mi", 446.20, 174.02},
		{"monitoring", "Deployment", "prometheus", "200m", "150m", "512Mi", "320Mi", 75.0, 62.5, "50m", "192Mi", 958.30, 239.58},
		{"monitoring", "Deployment", "grafana", "100m", "55m", "128Mi", "90Mi", 55.0, 70.3, "45m", "38Mi", 389.30, 116.79},
		{"kube-system", "DaemonSet", "kube-proxy", "100m", "30m", "128Mi", "45Mi", 30.0, 35.2, "70m", "83Mi", 312.40, 203.06},
		{"kube-system", "Deployment", "coredns", "100m", "20m", "70Mi", "35Mi", 20.0, 50.0, "80m", "35Mi", 174.80, 87.40},
		{"default", "Deployment", "koptimizer", "100m", "40m", "128Mi", "75Mi", 40.0, 58.6, "60m", "53Mi", 100.00, 41.40},
		{"default", "Deployment", "koptimizer-dashboard", "50m", "10m", "64Mi", "25Mi", 20.0, 39.1, "40m", "39Mi", 50.00, 30.05},
	}
	result := make([]map[string]any, len(effData))
	for i, e := range effData {
		result[i] = map[string]any{
			"namespace": e.ns, "kind": e.kind, "name": e.name,
			"cpuRequest": e.cpuReq, "cpuUsed": e.cpuUsed,
			"memRequest": e.memReq, "memUsed": e.memUsed,
			"cpuEfficiencyPct": e.cpuEffPct, "memEfficiencyPct": e.memEffPct,
			"wastedCPU": e.wastedCPU, "wastedMem": e.wastedMem,
			"monthlyCostUSD": e.monthlyCostUSD, "wastedCostUSD": e.wastedCostUSD,
		}
	}
	return map[string]any{
		"workloads": result,
		"summary": map[string]any{
			"avgCPUEfficiency": 54.3,
			"avgMemEfficiency": 64.1,
			"totalWastedCostUSD": 1231.26,
		},
	}
}

// ── Idle Resources ──
func idleResources() any {
	return map[string]any{
		"summary": map[string]any{
			"totalIdleNodes":     2,
			"totalIdleWorkloads": 4,
			"totalWastedCostUSD": 892.40,
			"avgIdleDurationHrs": 18.5,
		},
		"nodes": []map[string]any{
			{"name": "ip-10-0-4-102.ec2", "instanceType": "r5.xlarge", "cpuUtilPct": 8.2, "memUtilPct": 12.4, "idleSinceHrs": 24, "hourlyCostUSD": 0.252, "wastedCostUSD": 181.44, "reason": "CPU and memory below 15% for 24+ hours"},
			{"name": "ip-10-0-6-102.ec2", "instanceType": "p3.2xlarge", "cpuUtilPct": 3.1, "memUtilPct": 5.0, "idleSinceHrs": 36, "hourlyCostUSD": 3.06, "wastedCostUSD": 330.48, "reason": "GPU node with no active GPU workloads for 36+ hours"},
		},
		"workloads": []map[string]any{
			{"namespace": "staging", "kind": "Deployment", "name": "staging-app", "cpuUsedPct": 12.0, "memUsedPct": 18.5, "replicas": 2, "idleSinceHrs": 48, "wastedCostUSD": 156.80, "reason": "Staging workload with minimal traffic for 48+ hours"},
			{"namespace": "default", "kind": "Deployment", "name": "koptimizer-dashboard", "cpuUsedPct": 8.0, "memUsedPct": 15.6, "replicas": 1, "idleSinceHrs": 12, "wastedCostUSD": 24.00, "reason": "Very low utilization, consider reducing resources"},
			{"namespace": "kube-system", "kind": "DaemonSet", "name": "kube-proxy", "cpuUsedPct": 5.0, "memUsedPct": 8.2, "replicas": 12, "idleSinceHrs": 72, "wastedCostUSD": 156.00, "reason": "Consistently low utilization across all nodes"},
			{"namespace": "monitoring", "kind": "Deployment", "name": "grafana", "cpuUsedPct": 14.0, "memUsedPct": 22.0, "replicas": 1, "idleSinceHrs": 8, "wastedCostUSD": 44.08, "reason": "Low dashboard usage, consider scaling down"},
		},
		"orphanedPVCs": []map[string]any{
			{"name": "data-old-postgres-0", "namespace": "staging", "sizeGB": 100, "mountedBy": "", "ageHours": 720, "monthlyCostUSD": 10.00},
			{"name": "logs-deprecated-app", "namespace": "default", "sizeGB": 50, "mountedBy": "", "ageHours": 360, "monthlyCostUSD": 5.00},
		},
	}
}

// ── Cost Impact ──
func costImpact() any {
	now := time.Now()
	// Generate 12 months of savings data
	months := make([]map[string]any, 12)
	cumulativeSavings := 0.0
	for i := 0; i < 12; i++ {
		month := now.AddDate(0, -11+i, 0)
		monthlySaving := 800.0 + float64(i)*120.0 + jitter(0, 100)
		cumulativeSavings += monthlySaving
		months[i] = map[string]any{
			"month":             month.Format("2006-01"),
			"savingsUSD":        math.Round(monthlySaving*100) / 100,
			"cumulativeSavings": math.Round(cumulativeSavings*100) / 100,
			"actionsApplied":    3 + rand.Intn(8),
		}
	}
	return map[string]any{
		"summary": map[string]any{
			"totalSavingsUSD":        math.Round(cumulativeSavings*100) / 100,
			"avgMonthlySavingsUSD":   math.Round(cumulativeSavings/12*100) / 100,
			"totalActionsApplied":    87,
			"savingsVsIdentifiedPct": 68.4,
			"roiMultiple":            4.2,
		},
		"monthly": months,
		"byCategory": []map[string]any{
			{"category": "Rightsizing", "savingsUSD": math.Round(cumulativeSavings * 0.35 * 100) / 100, "actionsApplied": 32, "color": "#4361ee"},
			{"category": "Spot Migration", "savingsUSD": math.Round(cumulativeSavings * 0.28 * 100) / 100, "actionsApplied": 12, "color": "#10b981"},
			{"category": "Consolidation", "savingsUSD": math.Round(cumulativeSavings * 0.20 * 100) / 100, "actionsApplied": 18, "color": "#f59e0b"},
			{"category": "Commitments", "savingsUSD": math.Round(cumulativeSavings * 0.12 * 100) / 100, "actionsApplied": 15, "color": "#8b5cf6"},
			{"category": "Scaling", "savingsUSD": math.Round(cumulativeSavings * 0.05 * 100) / 100, "actionsApplied": 10, "color": "#ef4444"},
		},
		"recentActions": []map[string]any{
			{"timestamp": now.Add(-2 * time.Hour).Format(time.RFC3339), "action": "Rightsized web-frontend CPU", "savingsUSD": 45.60, "category": "Rightsizing"},
			{"timestamp": now.Add(-8 * time.Hour).Format(time.RFC3339), "action": "Migrated staging-app to spot", "savingsUSD": 580.00, "category": "Spot Migration"},
			{"timestamp": now.Add(-24 * time.Hour).Format(time.RFC3339), "action": "Consolidated node ip-10-0-2-101", "savingsUSD": 138.24, "category": "Consolidation"},
			{"timestamp": now.Add(-48 * time.Hour).Format(time.RFC3339), "action": "Applied RI for m5.xlarge", "savingsUSD": 276.10, "category": "Commitments"},
			{"timestamp": now.Add(-72 * time.Hour).Format(time.RFC3339), "action": "Scaled down worker HPA max", "savingsUSD": 92.30, "category": "Scaling"},
		},
	}
}

// ── Policies ──
func policiesData() any {
	return map[string]any{
		"nodeTemplates": []map[string]any{
			{
				"name": "general-purpose", "description": "Default node template for general workloads",
				"instanceFamilies": []string{"m5", "m6i", "m6a"}, "excludedTypes": []string{"m5.metal", "m6i.metal"},
				"architecture": "amd64", "capacityType": "on-demand",
				"minNodes": 2, "maxNodes": 8, "zones": []string{"us-east-1a", "us-east-1b"},
				"taints": []map[string]any{}, "labels": map[string]any{"tier": "general", "env": "production"},
			},
			{
				"name": "compute-optimized", "description": "For CPU-intensive workloads",
				"instanceFamilies": []string{"c5", "c6i"}, "excludedTypes": []string{},
				"architecture": "amd64", "capacityType": "on-demand",
				"minNodes": 1, "maxNodes": 6, "zones": []string{"us-east-1a", "us-east-1b", "us-east-1c"},
				"taints": []map[string]any{{"key": "workload", "value": "compute", "effect": "NoSchedule"}},
				"labels": map[string]any{"tier": "compute", "env": "production"},
			},
			{
				"name": "memory-optimized", "description": "For memory-intensive workloads",
				"instanceFamilies": []string{"r5", "r6i"}, "excludedTypes": []string{},
				"architecture": "amd64", "capacityType": "on-demand",
				"minNodes": 1, "maxNodes": 5, "zones": []string{"us-east-1a", "us-east-1b", "us-east-1c"},
				"taints": []map[string]any{},
				"labels": map[string]any{"tier": "memory", "env": "production"},
			},
			{
				"name": "spot-workers", "description": "Cost-optimized spot instances for non-critical workloads",
				"instanceFamilies": []string{"m5", "m6i", "c5", "r5"}, "excludedTypes": []string{},
				"architecture": "amd64", "capacityType": "spot",
				"minNodes": 0, "maxNodes": 10, "zones": []string{"us-east-1a", "us-east-1b"},
				"taints": []map[string]any{{"key": "kubernetes.io/spot", "value": "true", "effect": "PreferNoSchedule"}},
				"labels": map[string]any{"tier": "spot", "env": "non-critical"},
			},
		},
		"schedulingPolicies": []map[string]any{
			{"name": "High Availability", "description": "Spread critical workloads across AZs", "type": "topology-spread", "target": "production/*", "enabled": true},
			{"name": "GPU Affinity", "description": "Schedule GPU workloads only on GPU nodes", "type": "node-affinity", "target": "gpu/*", "enabled": true},
			{"name": "Spot Tolerance", "description": "Allow non-critical workloads on spot instances", "type": "toleration", "target": "staging/*", "enabled": true},
			{"name": "Memory-intensive Packing", "description": "Bin-pack memory-heavy pods on r5 nodes", "type": "bin-packing", "target": "production/cache,production/api-server", "enabled": false},
			{"name": "Cost-aware Scheduling", "description": "Prefer cheaper nodes when resource requirements are flexible", "type": "cost-aware", "target": "*/*", "enabled": true},
		},
	}
}

// ── Spot ──
func spotSummary() any {
	return map[string]any{
		"spotNodes":                  2,
		"onDemandNodes":              10,
		"spotPercentage":             16.7,
		"estimatedMonthlySavingsUSD": 580.00,
		"spotHourlyCostUSD":          0.52,
		"onDemandHourlyCostUSD":      8.40,
	}
}

func spotNodes() any {
	return []map[string]any{
		{"name": "ip-10-0-5-201.ec2", "instanceType": "m5.xlarge", "lifecycle": "spot", "zone": "us-east-1a", "hourlyCostUSD": 0.26},
		{"name": "ip-10-0-5-202.ec2", "instanceType": "m5.xlarge", "lifecycle": "spot", "zone": "us-east-1b", "hourlyCostUSD": 0.26},
		{"name": "ip-10-0-1-101.ec2", "instanceType": "m5.xlarge", "lifecycle": "on-demand", "zone": "us-east-1a", "hourlyCostUSD": 0.84},
		{"name": "ip-10-0-1-102.ec2", "instanceType": "m5.2xlarge", "lifecycle": "on-demand", "zone": "us-east-1a", "hourlyCostUSD": 1.68},
		{"name": "ip-10-0-2-101.ec2", "instanceType": "c5.xlarge", "lifecycle": "on-demand", "zone": "us-east-1b", "hourlyCostUSD": 0.72},
	}
}

// ── GPU ──
func gpuNodes() any {
	return []map[string]any{
		{"name": "ip-10-0-6-101.ec2", "instanceType": "p3.2xlarge", "gpuCount": 1, "gpuUsed": 1, "cpuUtilPct": 65.0, "memUtilPct": 58.0, "hourlyCostUSD": 3.06},
		{"name": "ip-10-0-6-102.ec2", "instanceType": "p3.2xlarge", "gpuCount": 1, "gpuUsed": 0, "cpuUtilPct": 12.0, "memUtilPct": 15.0, "hourlyCostUSD": 3.06},
	}
}

func gpuUtil() any {
	return map[string]any{"totalGPUs": 2, "usedGPUs": 1, "utilizationPct": 50.0}
}

func gpuRecs() any {
	return []map[string]any{
		{"type": "gpu-idle", "target": "ip-10-0-6-102.ec2", "description": "GPU node idle for >30m. Consider CPU fallback or termination", "estimatedSavings": 2203.20},
	}
}

// ── Commitments ──
func allCommitments() any {
	return []map[string]any{
		{"id": "ri-001", "type": "Reserved Instance", "instanceType": "m5.xlarge", "hourlyCostUSD": 0.124, "utilizationPct": 95.0, "expiresAt": "2027-03-15"},
		{"id": "ri-002", "type": "Reserved Instance", "instanceType": "c5.2xlarge", "hourlyCostUSD": 0.218, "utilizationPct": 88.0, "expiresAt": "2026-08-22"},
		{"id": "ri-003", "type": "Savings Plan", "instanceType": "General Compute", "hourlyCostUSD": 0.350, "utilizationPct": 42.0, "expiresAt": "2027-01-10"},
		{"id": "ri-004", "type": "Reserved Instance", "instanceType": "r5.xlarge", "hourlyCostUSD": 0.165, "utilizationPct": 78.0, "expiresAt": "2026-04-05"},
	}
}

func underutilizedCommitments() any {
	return []map[string]any{
		{"id": "ri-003", "type": "Savings Plan", "instanceType": "General Compute", "hourlyCostUSD": 0.350, "utilizationPct": 42.0, "expiresAt": "2027-01-10"},
	}
}

func expiringCommitments() any {
	return []map[string]any{
		{"id": "ri-004", "type": "Reserved Instance", "instanceType": "r5.xlarge", "hourlyCostUSD": 0.165, "utilizationPct": 78.0, "expiresAt": "2026-04-05"},
	}
}

// ── Audit ──
func auditEvents() any {
	now := time.Now()
	return []map[string]any{
		{"timestamp": now.Add(-10 * time.Minute).Format(time.RFC3339), "action": "recommendation.approved", "target": "rec-004", "user": "admin", "details": "Approved rightsizing for web-frontend: reduce CPU limit from 200m to 120m"},
		{"timestamp": now.Add(-25 * time.Minute).Format(time.RFC3339), "action": "recommendation.approved", "target": "rec-006", "user": "admin", "details": "Approved scaling change for worker: increase HPA max from 4 to 6"},
		{"timestamp": now.Add(-1 * time.Hour).Format(time.RFC3339), "action": "recommendation.dismissed", "target": "rec-007", "user": "admin", "details": "Dismissed rightsizing for prometheus: memory reduction too aggressive"},
		{"timestamp": now.Add(-2 * time.Hour).Format(time.RFC3339), "action": "mode.changed", "target": "cluster", "user": "admin", "details": "Operating mode changed from monitor to recommend"},
		{"timestamp": now.Add(-4 * time.Hour).Format(time.RFC3339), "action": "node.scaled", "target": "ng-spot-1", "user": "system", "details": "Node group spot-workers scaled from 1 to 2 nodes"},
		{"timestamp": now.Add(-6 * time.Hour).Format(time.RFC3339), "action": "workload.rightsized", "target": "production/cache", "user": "system", "details": "Applied rightsizing to cache: CPU request reduced from 200m to 100m"},
		{"timestamp": now.Add(-12 * time.Hour).Format(time.RFC3339), "action": "config.updated", "target": "aiGate", "user": "admin", "details": "AI Gate controller disabled"},
		{"timestamp": now.Add(-24 * time.Hour).Format(time.RFC3339), "action": "recommendation.approved", "target": "rec-003", "user": "admin", "details": "Approved consolidation for node ip-10-0-2-101"},
		{"timestamp": now.Add(-36 * time.Hour).Format(time.RFC3339), "action": "node.scaled", "target": "ng-compute-1", "user": "system", "details": "Node group compute-optimized scaled from 2 to 3 nodes"},
		{"timestamp": now.Add(-48 * time.Hour).Format(time.RFC3339), "action": "mode.changed", "target": "cluster", "user": "admin", "details": "Operating mode changed from enforce to monitor"},
	}
}

// ── Notifications ──
func notificationsData() any {
	now := time.Now()
	return map[string]any{
		"alerts": []map[string]any{
			{"timestamp": now.Add(-5 * time.Minute).Format(time.RFC3339), "severity": "critical", "category": "resource", "message": "Node ip-10-0-4-102 memory utilization above 90% for 15 minutes", "target": "ip-10-0-4-102.ec2", "status": "active"},
			{"timestamp": now.Add(-20 * time.Minute).Format(time.RFC3339), "severity": "warning", "category": "cost", "message": "Savings plan ri-003 utilization dropped below 50%", "target": "ri-003", "status": "active"},
			{"timestamp": now.Add(-45 * time.Minute).Format(time.RFC3339), "severity": "warning", "category": "scaling", "message": "HPA for worker at max replicas for 30+ minutes", "target": "production/worker", "status": "active"},
			{"timestamp": now.Add(-1 * time.Hour).Format(time.RFC3339), "severity": "info", "category": "optimization", "message": "New rightsizing recommendation available for cache", "target": "production/cache", "status": "active"},
			{"timestamp": now.Add(-2 * time.Hour).Format(time.RFC3339), "severity": "critical", "category": "resource", "message": "Pod OOMKilled: worker-9f6e5d4c3-z8a2b", "target": "production/worker", "status": "resolved"},
			{"timestamp": now.Add(-4 * time.Hour).Format(time.RFC3339), "severity": "warning", "category": "cost", "message": "Monthly cost trending 8% above forecast", "target": "cluster", "status": "resolved"},
			{"timestamp": now.Add(-8 * time.Hour).Format(time.RFC3339), "severity": "info", "category": "scaling", "message": "Node group spot-workers scaled to 2 nodes", "target": "ng-spot-1", "status": "resolved"},
		},
		"channels": []map[string]any{
			{"type": "slack", "name": "Slack #k8s-alerts", "target": "#k8s-alerts", "enabled": true},
			{"type": "webhook", "name": "PagerDuty Critical", "target": "https://events.pagerduty.com/...", "enabled": true},
			{"type": "webhook", "name": "Custom Webhook", "target": "https://hooks.example.com/kopt", "enabled": false},
		},
	}
}

// ── Events ──
func eventsData() any {
	now := time.Now()
	events := []map[string]any{
		{"timestamp": now.Add(-2 * time.Minute).Format(time.RFC3339), "action": "pod.evicted", "target": "staging/staging-app-xyz", "user": "system", "details": "Pod evicted due to node pressure on ip-10-0-5-101"},
		{"timestamp": now.Add(-10 * time.Minute).Format(time.RFC3339), "action": "recommendation.approved", "target": "rec-004", "user": "admin", "details": "Approved rightsizing for web-frontend: reduce CPU limit from 200m to 120m"},
		{"timestamp": now.Add(-15 * time.Minute).Format(time.RFC3339), "action": "hpa.scaled", "target": "production/worker", "user": "system", "details": "HPA scaled worker from 3 to 4 replicas due to CPU pressure"},
		{"timestamp": now.Add(-25 * time.Minute).Format(time.RFC3339), "action": "recommendation.approved", "target": "rec-006", "user": "admin", "details": "Approved scaling change for worker: increase HPA max from 4 to 6"},
		{"timestamp": now.Add(-30 * time.Minute).Format(time.RFC3339), "action": "cost.alert", "target": "cluster", "user": "system", "details": "Daily cost $298.40 exceeds budget threshold of $280"},
		{"timestamp": now.Add(-1 * time.Hour).Format(time.RFC3339), "action": "recommendation.dismissed", "target": "rec-007", "user": "admin", "details": "Dismissed rightsizing for prometheus: memory reduction too aggressive"},
		{"timestamp": now.Add(-90 * time.Minute).Format(time.RFC3339), "action": "node.cordoned", "target": "ip-10-0-2-101.ec2", "user": "system", "details": "Node cordoned for consolidation - pods being drained"},
		{"timestamp": now.Add(-2 * time.Hour).Format(time.RFC3339), "action": "mode.changed", "target": "cluster", "user": "admin", "details": "Operating mode changed from monitor to recommend"},
		{"timestamp": now.Add(-3 * time.Hour).Format(time.RFC3339), "action": "commitment.alert", "target": "ri-003", "user": "system", "details": "Savings plan utilization dropped to 42% - review coverage"},
		{"timestamp": now.Add(-4 * time.Hour).Format(time.RFC3339), "action": "node.scaled", "target": "ng-spot-1", "user": "system", "details": "Node group spot-workers scaled from 1 to 2 nodes"},
		{"timestamp": now.Add(-5 * time.Hour).Format(time.RFC3339), "action": "config.audit", "target": "default namespace", "user": "system", "details": "Resource quota missing for default namespace"},
		{"timestamp": now.Add(-6 * time.Hour).Format(time.RFC3339), "action": "workload.rightsized", "target": "production/cache", "user": "system", "details": "Applied rightsizing to cache: CPU request reduced from 200m to 100m"},
		{"timestamp": now.Add(-8 * time.Hour).Format(time.RFC3339), "action": "gpu.idle", "target": "ip-10-0-6-102.ec2", "user": "system", "details": "GPU node idle for >30 minutes - $3.06/hr wasted"},
		{"timestamp": now.Add(-12 * time.Hour).Format(time.RFC3339), "action": "config.updated", "target": "aiGate", "user": "admin", "details": "AI Gate controller disabled"},
		{"timestamp": now.Add(-18 * time.Hour).Format(time.RFC3339), "action": "network.alert", "target": "production/api-server", "user": "system", "details": "Cross-AZ traffic spike detected: 45.2 GB/month to us-east-1b"},
		{"timestamp": now.Add(-24 * time.Hour).Format(time.RFC3339), "action": "recommendation.approved", "target": "rec-003", "user": "admin", "details": "Approved consolidation for node ip-10-0-2-101"},
		{"timestamp": now.Add(-30 * time.Hour).Format(time.RFC3339), "action": "pdb.violation", "target": "production/api-server", "user": "system", "details": "PDB would be violated by eviction - blocked pod termination"},
		{"timestamp": now.Add(-36 * time.Hour).Format(time.RFC3339), "action": "node.scaled", "target": "ng-compute-1", "user": "system", "details": "Node group compute-optimized scaled from 2 to 3 nodes"},
		{"timestamp": now.Add(-48 * time.Hour).Format(time.RFC3339), "action": "mode.changed", "target": "cluster", "user": "admin", "details": "Operating mode changed from enforce to monitor"},
		{"timestamp": now.Add(-72 * time.Hour).Format(time.RFC3339), "action": "spot.interruption", "target": "ip-10-0-5-101.ec2", "user": "system", "details": "Spot interruption notice received - graceful drain initiated"},
	}
	return map[string]any{"events": events}
}

// ── Multi-Cluster ──
func clustersData() any {
	return map[string]any{
		"clusters": []map[string]any{
			{
				"id": "demo-cluster", "name": "demo-cluster", "provider": "aws", "region": "us-east-1",
				"nodeCount": 12, "podCount": 47, "monthlyCostUSD": 8247.50, "potentialSavings": 1834.20,
				"efficiencyScore": 68, "status": "healthy", "version": "1.28",
			},
			{
				"id": "staging-cluster", "name": "staging-cluster", "provider": "aws", "region": "us-west-2",
				"nodeCount": 4, "podCount": 18, "monthlyCostUSD": 2340.80, "potentialSavings": 890.40,
				"efficiencyScore": 52, "status": "healthy", "version": "1.28",
			},
			{
				"id": "eu-production", "name": "eu-production", "provider": "gcp", "region": "europe-west1",
				"nodeCount": 8, "podCount": 35, "monthlyCostUSD": 5680.20, "potentialSavings": 1240.60,
				"efficiencyScore": 74, "status": "healthy", "version": "1.29",
			},
		},
	}
}

// ── Prometheus Metrics ──
func metricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	metrics := `# HELP koptimizer_cluster_nodes_total Total number of nodes in the cluster
# TYPE koptimizer_cluster_nodes_total gauge
koptimizer_cluster_nodes_total 12
# HELP koptimizer_cluster_pods_total Total number of running pods
# TYPE koptimizer_cluster_pods_total gauge
koptimizer_cluster_pods_total 47
# HELP koptimizer_cluster_monthly_cost_usd Projected monthly cost in USD
# TYPE koptimizer_cluster_monthly_cost_usd gauge
koptimizer_cluster_monthly_cost_usd 8247.50
# HELP koptimizer_cluster_potential_savings_usd Identified potential monthly savings in USD
# TYPE koptimizer_cluster_potential_savings_usd gauge
koptimizer_cluster_potential_savings_usd 1834.20
# HELP koptimizer_cluster_cpu_utilization_pct Cluster CPU utilization percentage
# TYPE koptimizer_cluster_cpu_utilization_pct gauge
koptimizer_cluster_cpu_utilization_pct 62.3
# HELP koptimizer_cluster_memory_utilization_pct Cluster memory utilization percentage
# TYPE koptimizer_cluster_memory_utilization_pct gauge
koptimizer_cluster_memory_utilization_pct 71.8
# HELP koptimizer_cluster_efficiency_score Cluster efficiency score 0-100
# TYPE koptimizer_cluster_efficiency_score gauge
koptimizer_cluster_efficiency_score 68
# HELP koptimizer_cluster_score Cluster optimization score 0-10
# TYPE koptimizer_cluster_score gauge
koptimizer_cluster_score 7.2
# HELP koptimizer_recommendations_total Total recommendations by status
# TYPE koptimizer_recommendations_total gauge
koptimizer_recommendations_total{status="pending"} 4
koptimizer_recommendations_total{status="approved"} 2
koptimizer_recommendations_total{status="dismissed"} 1
# HELP koptimizer_nodegroup_nodes Node count per node group
# TYPE koptimizer_nodegroup_nodes gauge
koptimizer_nodegroup_nodes{name="general-purpose"} 4
koptimizer_nodegroup_nodes{name="compute-optimized"} 3
koptimizer_nodegroup_nodes{name="memory-optimized"} 3
koptimizer_nodegroup_nodes{name="spot-workers"} 2
# HELP koptimizer_nodegroup_cpu_utilization_pct CPU utilization per node group
# TYPE koptimizer_nodegroup_cpu_utilization_pct gauge
koptimizer_nodegroup_cpu_utilization_pct{name="general-purpose"} 58.0
koptimizer_nodegroup_cpu_utilization_pct{name="compute-optimized"} 78.0
koptimizer_nodegroup_cpu_utilization_pct{name="memory-optimized"} 42.0
koptimizer_nodegroup_cpu_utilization_pct{name="spot-workers"} 85.0
# HELP koptimizer_commitment_utilization_pct Commitment utilization percentage
# TYPE koptimizer_commitment_utilization_pct gauge
koptimizer_commitment_utilization_pct{id="ri-001",type="reserved_instance"} 95.0
koptimizer_commitment_utilization_pct{id="ri-002",type="reserved_instance"} 88.0
koptimizer_commitment_utilization_pct{id="ri-003",type="savings_plan"} 42.0
koptimizer_commitment_utilization_pct{id="ri-004",type="reserved_instance"} 78.0
# HELP koptimizer_network_cross_az_cost_usd Monthly cross-AZ traffic cost
# TYPE koptimizer_network_cross_az_cost_usd gauge
koptimizer_network_cross_az_cost_usd 278.40
# HELP koptimizer_gpu_utilization_pct GPU utilization percentage
# TYPE koptimizer_gpu_utilization_pct gauge
koptimizer_gpu_utilization_pct 50.0
`
	fmt.Fprint(w, metrics)
}
