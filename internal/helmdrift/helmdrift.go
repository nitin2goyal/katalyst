package helmdrift

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/koptimizer/koptimizer/internal/config"
	"github.com/koptimizer/koptimizer/internal/state"
	"github.com/koptimizer/koptimizer/pkg/cost"
)

// HelmResources represents parsed resource configuration from Helm values.
type HelmResources struct {
	CPURequestMilli  int64
	CPULimitMilli    int64
	MemRequestBytes  int64
	MemLimitBytes    int64
	Replicas         int
	MinReplicas      int
	MaxReplicas      int
}

// FieldDrift represents drift in a single field.
type FieldDrift struct {
	Field         string  `json:"field"`
	HelmValue     string  `json:"helmValue"`
	HelmWeekAgo   string  `json:"helmWeekAgo"`
	ActualValue   string  `json:"actualValue"`
	Drifted       bool    `json:"drifted"`
	CostImpactUSD float64 `json:"costImpactUSD"`
}

// WorkloadDrift represents drift for one workload.
type WorkloadDrift struct {
	ChartPath         string       `json:"chartPath"`
	Namespace         string       `json:"namespace"`
	Kind              string       `json:"kind"`
	Name              string       `json:"name"`
	Replicas          int          `json:"replicas"`
	Fields            []FieldDrift `json:"fields"`
	TotalCostImpact   float64      `json:"totalCostImpactUSD"`
	HelmFetchError    string       `json:"helmFetchError,omitempty"`
}

// DriftResult is the top-level API response.
type DriftResult struct {
	Workloads      []WorkloadDrift `json:"workloads"`
	TotalDrifted   int             `json:"totalDrifted"`
	TotalChecked   int             `json:"totalChecked"`
	TotalCostImpact float64        `json:"totalCostImpactUSD"`
	LastUpdated    time.Time       `json:"lastUpdated"`
}

// Service runs helm drift detection.
type Service struct {
	cfg          *config.Config
	clusterState *state.ClusterState
	httpClient   *http.Client

	mu         sync.RWMutex
	cache      *DriftResult
	lastUpdate time.Time
}

func NewService(cfg *config.Config, cs *state.ClusterState) *Service {
	return &Service{
		cfg:          cfg,
		clusterState: cs,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// GetDrift returns cached drift results, refreshing if stale (> 5 min).
func (s *Service) GetDrift(forceRefresh bool) (*DriftResult, error) {
	s.mu.RLock()
	if !forceRefresh && s.cache != nil && time.Since(s.lastUpdate) < 5*time.Minute {
		result := s.cache
		s.mu.RUnlock()
		return result, nil
	}
	s.mu.RUnlock()

	result, err := s.detectDrift()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache = result
	s.lastUpdate = time.Now()
	s.mu.Unlock()

	return result, nil
}

func (s *Service) detectDrift() (*DriftResult, error) {
	hd := s.cfg.HelmDrift
	if !hd.Enabled || hd.GitLabURL == "" || hd.GitLabToken == "" {
		return &DriftResult{LastUpdated: time.Now()}, nil
	}

	// Get actual workloads from cluster state
	pods := s.clusterState.GetAllPods()
	type wlKey struct{ ns, kind, name string }
	type wlInfo struct {
		Namespace   string
		Kind        string
		Name        string
		Replicas    int
		CPUReqMilli int64
		CPULimMilli int64
		MemReqBytes int64
		MemLimBytes int64
	}
	workloads := make(map[wlKey]*wlInfo)
	for _, p := range pods {
		if p.Pod.Status.Phase != "Running" && p.Pod.Status.Phase != "Pending" {
			continue
		}
		kind, name := resolveOwner(p)
		key := wlKey{p.Namespace, kind, name}
		wl, ok := workloads[key]
		if !ok {
			wl = &wlInfo{Namespace: p.Namespace, Kind: kind, Name: name}
			workloads[key] = wl
		}
		wl.Replicas++
		wl.CPUReqMilli += p.CPURequest
		wl.CPULimMilli += p.CPULimit
		wl.MemReqBytes += p.MemoryRequest
		wl.MemLimBytes += p.MemoryLimit
	}

	// Get commit SHA from ~1 week ago
	weekAgoSHA := s.getCommitBefore(hd, time.Now().AddDate(0, 0, -7))

	var drifts []WorkloadDrift
	totalDrifted := 0

	for _, chart := range hd.Charts {
		chartPath := chart.ChartPath
		if chart.Release != "" {
			chartPath += "/releases/" + chart.Release
		}

		// Fetch current helm values (base + release + env)
		helmRes, err := s.fetchAndParseValues(hd, chart, hd.Branch)
		if err != nil {
			slog.Warn("failed to fetch helm values", "chart", chartPath, "error", err)
			// Add error entry
			drifts = append(drifts, WorkloadDrift{
				ChartPath:      chartPath,
				HelmFetchError: err.Error(),
			})
			continue
		}

		// Fetch week-old helm values
		var helmResWeekAgo *HelmResources
		if weekAgoSHA != "" {
			helmResWeekAgo, _ = s.fetchAndParseValues(hd, chart, weekAgoSHA)
		}

		// Match with actual workloads
		matchName := chart.WorkloadName
		if matchName == "" {
			// Default: use release name or chart dir name
			if chart.Release != "" {
				matchName = chart.Release
			} else {
				parts := strings.Split(chart.ChartPath, "/")
				matchName = parts[len(parts)-1]
			}
		}

		for key, wl := range workloads {
			if chart.Namespace != "" && key.ns != chart.Namespace {
				continue
			}
			if !strings.Contains(strings.ToLower(key.name), strings.ToLower(matchName)) {
				continue
			}

			// Per-replica values from cluster
			perRepCPUReq := wl.CPUReqMilli
			perRepCPULim := wl.CPULimMilli
			perRepMemReq := wl.MemReqBytes
			perRepMemLim := wl.MemLimBytes
			if wl.Replicas > 0 {
				perRepCPUReq /= int64(wl.Replicas)
				perRepCPULim /= int64(wl.Replicas)
				perRepMemReq /= int64(wl.Replicas)
				perRepMemLim /= int64(wl.Replicas)
			}

			drift := WorkloadDrift{
				ChartPath: chartPath,
				Namespace: wl.Namespace,
				Kind:      wl.Kind,
				Name:      wl.Name,
				Replicas:  wl.Replicas,
			}

			// Compare fields
			drift.Fields = append(drift.Fields,
				compareField("cpuRequest", helmRes.CPURequestMilli, helmResWeekAgo, perRepCPUReq, fmtCPU, cpuCostImpact(s.cfg.CloudProvider, wl.Replicas)),
				compareField("cpuLimit", helmRes.CPULimitMilli, helmResWeekAgo, perRepCPULim, fmtCPU, nil),
				compareField("memRequest", helmRes.MemRequestBytes, helmResWeekAgo, perRepMemReq, fmtMem, memCostImpact(s.cfg.CloudProvider, wl.Replicas)),
				compareField("memLimit", helmRes.MemLimitBytes, helmResWeekAgo, perRepMemLim, fmtMem, nil),
			)

			// Replica drift
			if helmRes.Replicas > 0 {
				drift.Fields = append(drift.Fields,
					compareFieldInt("replicas", helmRes.Replicas, helmResWeekAgo, wl.Replicas, nil))
			} else if helmRes.MinReplicas > 0 {
				drift.Fields = append(drift.Fields,
					compareFieldInt("minReplicas", helmRes.MinReplicas, helmResWeekAgo, wl.Replicas, nil))
			}

			// Calculate total cost impact
			hasDrift := false
			for _, f := range drift.Fields {
				if f.Drifted {
					hasDrift = true
					drift.TotalCostImpact += math.Abs(f.CostImpactUSD)
				}
			}
			if hasDrift {
				totalDrifted++
			}

			drifts = append(drifts, drift)
		}
	}

	// Sort by cost impact descending
	for i := 1; i < len(drifts); i++ {
		for j := i; j > 0 && drifts[j].TotalCostImpact > drifts[j-1].TotalCostImpact; j-- {
			drifts[j], drifts[j-1] = drifts[j-1], drifts[j]
		}
	}

	totalCost := 0.0
	for _, d := range drifts {
		totalCost += d.TotalCostImpact
	}

	return &DriftResult{
		Workloads:       drifts,
		TotalDrifted:    totalDrifted,
		TotalChecked:    len(drifts),
		TotalCostImpact: totalCost,
		LastUpdated:     time.Now(),
	}, nil
}

// fetchAndParseValues fetches base + release + env values from GitLab and merges them.
func (s *Service) fetchAndParseValues(hd config.HelmDriftConfig, chart config.HelmChartConfig, ref string) (*HelmResources, error) {
	// Base values: <chartPath>/values.yaml
	basePath := chart.ChartPath + "/values.yaml"
	baseData, baseErr := s.fetchGitLabFile(hd, basePath, ref)

	// Release values: <chartPath>/releases/<release>/values.yaml
	var releaseData []byte
	if chart.Release != "" {
		releasePath := chart.ChartPath + "/releases/" + chart.Release + "/values.yaml"
		releaseData, _ = s.fetchGitLabFile(hd, releasePath, ref)
	}

	// Env values: <chartPath>/releases/<release>/env/<envFile>
	var envData []byte
	if chart.Release != "" && chart.EnvFile != "" {
		envPath := chart.ChartPath + "/releases/" + chart.Release + "/env/" + chart.EnvFile
		envData, _ = s.fetchGitLabFile(hd, envPath, ref)
	}

	if baseErr != nil && len(releaseData) == 0 {
		return nil, fmt.Errorf("failed to fetch base values: %w", baseErr)
	}

	return ParseHelmValues(baseData, releaseData, envData)
}

// fetchGitLabFile fetches a single file from GitLab API.
func (s *Service) fetchGitLabFile(hd config.HelmDriftConfig, filePath, ref string) ([]byte, error) {
	encodedProject := url.PathEscape(hd.ProjectPath)
	encodedFile := url.PathEscape(filePath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s/raw?ref=%s",
		strings.TrimRight(hd.GitLabURL, "/"), encodedProject, encodedFile, url.QueryEscape(ref))

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", hd.GitLabToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gitlab %d for %s: %s", resp.StatusCode, filePath, string(body))
	}

	return io.ReadAll(resp.Body)
}

// getCommitBefore finds the latest commit before a given time.
func (s *Service) getCommitBefore(hd config.HelmDriftConfig, before time.Time) string {
	encodedProject := url.PathEscape(hd.ProjectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/commits?ref_name=%s&until=%s&per_page=1",
		strings.TrimRight(hd.GitLabURL, "/"), encodedProject,
		url.QueryEscape(hd.Branch), url.QueryEscape(before.UTC().Format(time.RFC3339)))

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("PRIVATE-TOKEN", hd.GitLabToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return ""
	}

	var commits []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil || len(commits) == 0 {
		return ""
	}
	return commits[0].ID
}

// ── Values parsing ──────────────────────────────────────────────────────

// ParseHelmValues merges multiple YAML files (base → release → env) and extracts resource config.
func ParseHelmValues(yamlFiles ...[]byte) (*HelmResources, error) {
	merged := make(map[string]interface{})
	for _, data := range yamlFiles {
		if len(data) == 0 {
			continue
		}
		var m map[string]interface{}
		if err := yaml.Unmarshal(data, &m); err != nil {
			continue // skip unparseable files
		}
		deepMerge(merged, m)
	}

	res := &HelmResources{}

	// Pattern 1: Flat fields (most common in intuition-helm-charts)
	if v, ok := merged["cpuRequest"]; ok {
		res.CPURequestMilli = parseCPU(v)
	}
	if v, ok := merged["cpuLimit"]; ok {
		res.CPULimitMilli = parseCPU(v)
	}
	if v, ok := merged["memoryRequest"]; ok {
		res.MemRequestBytes = parseMem(v)
	}
	if v, ok := merged["memoryLimit"]; ok {
		res.MemLimitBytes = parseMem(v)
	}

	// Pattern 2: Nested resources (standard K8s or resourcesV2)
	if resources, ok := merged["resources"].(map[string]interface{}); ok {
		container := resources
		if c, ok := resources["container"].(map[string]interface{}); ok {
			container = c
		}
		// Flat within resources (lm/vectorizer pattern)
		if v, ok := container["cpuRequest"]; ok && res.CPURequestMilli == 0 {
			res.CPURequestMilli = parseCPU(v)
		}
		if v, ok := container["cpuLimit"]; ok && res.CPULimitMilli == 0 {
			res.CPULimitMilli = parseCPU(v)
		}
		if v, ok := container["memoryRequest"]; ok && res.MemRequestBytes == 0 {
			res.MemRequestBytes = parseMem(v)
		}
		if v, ok := container["memoryLimit"]; ok && res.MemLimitBytes == 0 {
			res.MemLimitBytes = parseMem(v)
		}
		// Standard K8s: resources.requests/limits
		if requests, ok := container["requests"].(map[string]interface{}); ok {
			if v, ok := requests["cpu"]; ok && res.CPURequestMilli == 0 {
				res.CPURequestMilli = parseCPU(v)
			}
			if v, ok := requests["memory"]; ok && res.MemRequestBytes == 0 {
				res.MemRequestBytes = parseMem(v)
			}
		}
		if limits, ok := container["limits"].(map[string]interface{}); ok {
			if v, ok := limits["cpu"]; ok && res.CPULimitMilli == 0 {
				res.CPULimitMilli = parseCPU(v)
			}
			if v, ok := limits["memory"]; ok && res.MemLimitBytes == 0 {
				res.MemLimitBytes = parseMem(v)
			}
		}
	}

	// Replicas
	if v, ok := merged["replicaCount"]; ok {
		res.Replicas = toInt(v)
	} else if v, ok := merged["replicas"]; ok {
		res.Replicas = toInt(v)
	}

	// Autoscaling
	if autoscale, ok := merged["autoscale"].(map[string]interface{}); ok {
		if v, ok := autoscale["minReplicaCount"]; ok {
			res.MinReplicas = toInt(v)
		}
		if v, ok := autoscale["maxReplicaCount"]; ok {
			res.MaxReplicas = toInt(v)
		}
	}
	if v, ok := merged["maxReplicas"]; ok && res.MaxReplicas == 0 {
		res.MaxReplicas = toInt(v)
	}

	return res, nil
}

// ── Comparison helpers ──────────────────────────────────────────────────

type costFn func(helmVal, actualVal int64) float64

func compareField(field string, helmVal int64, weekAgo *HelmResources, actualVal int64, fmtFn func(int64) string, costCalc costFn) FieldDrift {
	weekAgoVal := helmVal
	if weekAgo != nil {
		switch field {
		case "cpuRequest":
			weekAgoVal = weekAgo.CPURequestMilli
		case "cpuLimit":
			weekAgoVal = weekAgo.CPULimitMilli
		case "memRequest":
			weekAgoVal = weekAgo.MemRequestBytes
		case "memLimit":
			weekAgoVal = weekAgo.MemLimitBytes
		}
	}

	// Not drifted if actual matches current OR week-ago helm value
	drifted := helmVal > 0 && actualVal != helmVal && actualVal != weekAgoVal

	var costImpact float64
	if drifted && costCalc != nil {
		costImpact = costCalc(helmVal, actualVal)
	}

	return FieldDrift{
		Field:         field,
		HelmValue:     fmtFn(helmVal),
		HelmWeekAgo:   fmtFn(weekAgoVal),
		ActualValue:   fmtFn(actualVal),
		Drifted:       drifted,
		CostImpactUSD: costImpact,
	}
}

func compareFieldInt(field string, helmVal int, weekAgo *HelmResources, actualVal int, costCalc costFn) FieldDrift {
	weekAgoVal := helmVal
	if weekAgo != nil {
		switch field {
		case "replicas":
			weekAgoVal = weekAgo.Replicas
		case "minReplicas":
			weekAgoVal = weekAgo.MinReplicas
		case "maxReplicas":
			weekAgoVal = weekAgo.MaxReplicas
		}
	}

	drifted := helmVal > 0 && actualVal != helmVal && actualVal != weekAgoVal

	return FieldDrift{
		Field:       field,
		HelmValue:   strconv.Itoa(helmVal),
		HelmWeekAgo: strconv.Itoa(weekAgoVal),
		ActualValue: strconv.Itoa(actualVal),
		Drifted:     drifted,
	}
}

func cpuCostImpact(cloudProvider string, replicas int) costFn {
	return func(helmVal, actualVal int64) float64 {
		delta := float64(actualVal-helmVal) / 1000.0 // vCPUs
		rate := vCPUHourlyCost(cloudProvider)
		return delta * rate * cost.HoursPerMonth * float64(replicas)
	}
}

func memCostImpact(cloudProvider string, replicas int) costFn {
	return func(helmVal, actualVal int64) float64 {
		deltaGiB := float64(actualVal-helmVal) / (1024 * 1024 * 1024)
		rate := memGiBHourlyCost(cloudProvider)
		return deltaGiB * rate * cost.HoursPerMonth * float64(replicas)
	}
}

func vCPUHourlyCost(cp string) float64 {
	switch cp {
	case "gcp":
		return 0.031611
	case "azure":
		return 0.043
	default:
		return 0.02
	}
}

func memGiBHourlyCost(cp string) float64 {
	switch cp {
	case "gcp":
		return 0.004237
	case "azure":
		return 0.005
	default:
		return 0.00322
	}
}

// ── Owner resolution (mirrors handler/workload.go) ──────────────────────

func resolveOwner(p *state.PodState) (kind, name string) {
	kind, name = p.OwnerKind, p.OwnerName
	if kind == "ReplicaSet" && p.Pod != nil {
		if hash, ok := p.Pod.Labels["pod-template-hash"]; ok && strings.HasSuffix(name, "-"+hash) {
			kind = "Deployment"
			name = strings.TrimSuffix(name, "-"+hash)
		}
	}
	if name == "" {
		name = p.Name
		kind = "Pod"
	}
	return
}

// ── YAML helpers ────────────────────────────────────────────────────────

func deepMerge(dst, src map[string]interface{}) {
	for k, v := range src {
		if srcMap, ok := v.(map[string]interface{}); ok {
			if dstMap, ok := dst[k].(map[string]interface{}); ok {
				deepMerge(dstMap, srcMap)
				continue
			}
		}
		dst[k] = v
	}
}

var memRegex = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(Gi|Mi|Ki|G|M|K|Ti|gi|mi|ki)?$`)

func parseCPU(v interface{}) int64 {
	switch n := v.(type) {
	case int:
		return int64(n) * 1000
	case int64:
		return n * 1000
	case float64:
		return int64(n * 1000)
	case string:
		s := strings.TrimSpace(n)
		if strings.HasSuffix(s, "m") {
			if val, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64); err == nil {
				return int64(val)
			}
		}
		if val, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(val * 1000)
		}
	}
	return 0
}

func parseMem(v interface{}) int64 {
	switch n := v.(type) {
	case int:
		// Bare integer — context-dependent. Could be GB for resourcesV2.
		// Treat as bytes if large, GB if small.
		if n > 1024 {
			return int64(n) // already bytes
		}
		return int64(n) * 1024 * 1024 * 1024 // assume GB
	case int64:
		if n > 1024 {
			return n
		}
		return n * 1024 * 1024 * 1024
	case float64:
		if n > 1024 {
			return int64(n)
		}
		return int64(n * 1024 * 1024 * 1024)
	case string:
		s := strings.TrimSpace(n)
		m := memRegex.FindStringSubmatch(s)
		if m == nil {
			return 0
		}
		val, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0
		}
		switch strings.ToLower(m[2]) {
		case "ti":
			return int64(val * 1024 * 1024 * 1024 * 1024)
		case "gi", "g":
			return int64(val * 1024 * 1024 * 1024)
		case "mi", "m":
			return int64(val * 1024 * 1024)
		case "ki", "k":
			return int64(val * 1024)
		default:
			return int64(val * 1024 * 1024 * 1024) // default to Gi
		}
	}
	return 0
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		if val, err := strconv.Atoi(n); err == nil {
			return val
		}
	}
	return 0
}

func fmtCPU(milli int64) string {
	if milli == 0 {
		return "-"
	}
	if milli >= 1000 && milli%1000 == 0 {
		return fmt.Sprintf("%d", milli/1000)
	}
	return fmt.Sprintf("%dm", milli)
}

func fmtMem(bytes int64) string {
	if bytes == 0 {
		return "-"
	}
	const (
		gi = 1024 * 1024 * 1024
		mi = 1024 * 1024
	)
	if bytes >= gi && bytes%gi == 0 {
		return fmt.Sprintf("%dGi", bytes/gi)
	}
	if bytes >= mi {
		return fmt.Sprintf("%dMi", bytes/mi)
	}
	return fmt.Sprintf("%d", bytes)
}
