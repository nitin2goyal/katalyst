package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
)

const (
	serverName      = "koptimizer-mcp"
	serverVersion   = "0.1.0"
	protocolVersion = "2024-11-05"
)

// MCPServer is the MCP server that bridges stdio JSON-RPC to the KOptimizer REST API.
type MCPServer struct {
	client *APIClient
	tools  []Tool
	logger *log.Logger
}

// NewMCPServer creates a new MCPServer with the given API base URL.
func NewMCPServer(baseURL string) *MCPServer {
	return &MCPServer{
		client: NewAPIClient(baseURL),
		tools:  AllTools(),
		logger: log.New(os.Stderr, "[koptimizer-mcp] ", log.LstdFlags),
	}
}

// Run starts the stdio JSON-RPC loop. It reads requests from stdin, dispatches
// them, and writes responses to stdout. It blocks until stdin is closed.
func (s *MCPServer) Run() error {
	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout

	s.logger.Println("MCP server starting, reading from stdin")

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				s.logger.Println("stdin closed, shutting down")
				return nil
			}
			return fmt.Errorf("reading stdin: %w", err)
		}

		// Skip empty lines
		if len(line) == 0 || (len(line) == 1 && line[0] == '\n') {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(writer, nil, ErrCodeParseError, "Parse error: "+err.Error())
			continue
		}

		s.logger.Printf("received method=%s id=%s", req.Method, string(req.ID))

		resp := s.dispatch(&req)
		s.writeResponse(writer, resp)
	}
}

// dispatch routes a JSON-RPC request to the appropriate handler.
func (s *MCPServer) dispatch(req *Request) *Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "initialized":
		// Notification, no response required per MCP spec.
		// Return nil to signal no response should be written.
		return nil
	case "notifications/initialized":
		return nil
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	case "ping":
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]interface{}{},
		}
	default:
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &RPCError{
				Code:    ErrCodeMethodNotFound,
				Message: fmt.Sprintf("Method not found: %s", req.Method),
			},
		}
	}
}

// handleInitialize handles the "initialize" method.
func (s *MCPServer) handleInitialize(req *Request) *Response {
	result := InitializeResult{
		ProtocolVersion: protocolVersion,
		Capabilities: ServerCaps{
			Tools: &ToolsCap{},
		},
		ServerInfo: ServerInfo{
			Name:    serverName,
			Version: serverVersion,
		},
		Instructions: "KOptimizer MCP server. Provides tools to query and manage Kubernetes cluster cost optimization. Connect to a running KOptimizer instance to use these tools.",
	}
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// handleToolsList handles the "tools/list" method.
func (s *MCPServer) handleToolsList(req *Request) *Response {
	result := ToolsListResult{
		Tools: s.tools,
	}
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// handleToolsCall handles the "tools/call" method.
func (s *MCPServer) handleToolsCall(req *Request) *Response {
	var params ToolCallParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &RPCError{
					Code:    ErrCodeInvalidParams,
					Message: "Invalid params: " + err.Error(),
				},
			}
		}
	}

	if params.Name == "" {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &RPCError{
				Code:    ErrCodeInvalidParams,
				Message: "Missing required parameter: name",
			},
		}
	}

	s.logger.Printf("calling tool: %s", params.Name)

	result, apiErr := s.executeTool(params.Name, params.Arguments)
	if apiErr != nil {
		s.logger.Printf("tool %s error: %v", params.Name, apiErr)
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ToolCallResult{
				Content: []TextContent{
					{Type: "text", Text: fmt.Sprintf("Error: %s", apiErr.Error())},
				},
				IsError: true,
			},
		}
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: ToolCallResult{
			Content: []TextContent{
				{Type: "text", Text: string(result)},
			},
		},
	}
}

// executeTool dispatches to the correct API client method based on the tool name.
func (s *MCPServer) executeTool(name string, args map[string]interface{}) (json.RawMessage, error) {
	// Helper to extract a string argument.
	getString := func(key string) (string, error) {
		v, ok := args[key]
		if !ok {
			return "", fmt.Errorf("missing required argument: %s", key)
		}
		str, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("argument %s must be a string", key)
		}
		return str, nil
	}

	switch name {
	// ── Cluster ──
	case "get_cluster_summary":
		return s.client.GetClusterSummary()
	case "get_cluster_health":
		return s.client.GetClusterHealth()

	// ── Node Groups ──
	case "list_nodegroups":
		return s.client.ListNodeGroups()
	case "get_nodegroup":
		id, err := getString("id")
		if err != nil {
			return nil, err
		}
		return s.client.GetNodeGroup(id)
	case "get_nodegroup_nodes":
		id, err := getString("id")
		if err != nil {
			return nil, err
		}
		return s.client.GetNodeGroupNodes(id)
	case "list_empty_nodegroups":
		return s.client.ListEmptyNodeGroups()

	// ── Nodes ──
	case "list_nodes":
		return s.client.ListNodes()
	case "get_node":
		name, err := getString("name")
		if err != nil {
			return nil, err
		}
		return s.client.GetNode(name)

	// ── Cost ──
	case "get_cost_summary":
		return s.client.GetCostSummary()
	case "get_cost_by_namespace":
		return s.client.GetCostByNamespace()
	case "get_cost_by_workload":
		return s.client.GetCostByWorkload()
	case "get_cost_by_label":
		return s.client.GetCostByLabel()
	case "get_cost_trend":
		return s.client.GetCostTrend()
	case "get_cost_savings":
		return s.client.GetCostSavings()

	// ── Commitments ──
	case "list_commitments":
		return s.client.ListCommitments()
	case "list_underutilized_commitments":
		return s.client.ListUnderutilizedCommitments()
	case "list_expiring_commitments":
		return s.client.ListExpiringCommitments()

	// ── Recommendations ──
	case "list_recommendations":
		return s.client.ListRecommendations()
	case "get_recommendation":
		id, err := getString("id")
		if err != nil {
			return nil, err
		}
		return s.client.GetRecommendation(id)
	case "approve_recommendation":
		id, err := getString("id")
		if err != nil {
			return nil, err
		}
		return s.client.ApproveRecommendation(id)
	case "dismiss_recommendation":
		id, err := getString("id")
		if err != nil {
			return nil, err
		}
		return s.client.DismissRecommendation(id)
	case "get_recommendations_summary":
		return s.client.GetRecommendationsSummary()

	// ── Workloads ──
	case "list_workloads":
		return s.client.ListWorkloads()
	case "get_workload":
		ns, err := getString("namespace")
		if err != nil {
			return nil, err
		}
		kind, err := getString("kind")
		if err != nil {
			return nil, err
		}
		wlName, err := getString("name")
		if err != nil {
			return nil, err
		}
		return s.client.GetWorkload(ns, kind, wlName)
	case "get_workload_rightsizing":
		ns, err := getString("namespace")
		if err != nil {
			return nil, err
		}
		kind, err := getString("kind")
		if err != nil {
			return nil, err
		}
		wlName, err := getString("name")
		if err != nil {
			return nil, err
		}
		return s.client.GetWorkloadRightsizing(ns, kind, wlName)
	case "get_workload_scaling":
		ns, err := getString("namespace")
		if err != nil {
			return nil, err
		}
		kind, err := getString("kind")
		if err != nil {
			return nil, err
		}
		wlName, err := getString("name")
		if err != nil {
			return nil, err
		}
		return s.client.GetWorkloadScaling(ns, kind, wlName)

	// ── GPU ──
	case "list_gpu_nodes":
		return s.client.ListGPUNodes()
	case "get_gpu_utilization":
		return s.client.GetGPUUtilization()
	case "list_gpu_recommendations":
		return s.client.ListGPURecommendations()

	// ── Config ──
	case "get_config":
		return s.client.GetConfig()
	case "set_mode":
		mode, err := getString("mode")
		if err != nil {
			return nil, err
		}
		return s.client.SetMode(mode)

	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// writeResponse writes a JSON-RPC response to the writer as a single JSON line.
func (s *MCPServer) writeResponse(w io.Writer, resp *Response) {
	if resp == nil {
		// Notifications don't get a response.
		return
	}
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Printf("ERROR: failed to marshal response: %v", err)
		return
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		s.logger.Printf("ERROR: failed to write response: %v", err)
	}
}

// writeError writes a JSON-RPC error response to the writer.
func (s *MCPServer) writeError(w io.Writer, id json.RawMessage, code int, message string) {
	resp := &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
	}
	s.writeResponse(w, resp)
}
