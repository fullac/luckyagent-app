// Package luckyagent provides gRPC server implementation for LuckyAgent.
package luckyagent

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/yurika0211/lucky-agent/agent"
	transport "github.com/yurika0211/luckyagent-api"
	"github.com/yurika0211/luckyagent-runtime/rag"
	"github.com/yurika0211/luckyagent-runtime/session"
	"github.com/yurika0211/luckyagent-runtime/workflow"
	"github.com/yurika0211/luckyagent-tools/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// memoryAPIStore is the owner administration surface actually used by gRPC.
type memoryAPIStore interface {
	memory.Reader
	memory.WriteBackend
	Get(string) (*memory.Entry, error)
	Recent(int) []memory.Entry
	Count() int
	Delete(string) error
}

// Server implements LuckyAgentService gRPC server.
type Server struct {
	UnimplementedLuckyAgentServiceServer

	agent          *agent.Agent
	memoryStore    memoryAPIStore
	ragManager     *rag.RAGManager
	workflowEngine *workflow.WorkflowEngine

	mu      sync.RWMutex
	startAt time.Time
}

// NewServer creates a new gRPC server.
func NewServer(
	a *agent.Agent,
	ms memoryAPIStore,
	rm *rag.RAGManager,
	we *workflow.WorkflowEngine,
) *Server {
	return &Server{
		agent:          a,
		memoryStore:    ms,
		ragManager:     rm,
		workflowEngine: we,
		startAt:        time.Now(),
	}
}

// Chat handles single chat request.
func (s *Server) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	sess, loopCfg, err := s.prepareChat(ctx, req)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	result, err := s.agent.RunLoopWithSessionInput(ctx, sess, agent.TextUserTurnInput(req.Message), loopCfg)
	if err != nil {
		return nil, chatStatusError(err)
	}
	resp := &ChatResponse{
		Response:   result.Response,
		SessionId:  sess.ID,
		Iterations: int32(result.Iterations),
		TokensUsed: int32(result.TokensUsed),
		Duration:   time.Since(start).String(),
	}
	for _, call := range result.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, &ToolCall{
			Name: call.Name, Arguments: call.Arguments, Result: call.Result, Duration: call.Duration.String(),
		})
	}
	return resp, nil
}

// ChatStream handles streaming chat request.
func (s *Server) ChatStream(req *ChatRequest, stream LuckyAgentService_ChatStreamServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	sess, loopCfg, err := s.prepareChat(ctx, req)
	if err != nil {
		return err
	}
	events, err := s.agent.ChatWithSessionStreamForSession(ctx, sess, agent.TextUserTurnInput(req.Message), loopCfg)
	if err != nil {
		return chatStatusError(err)
	}
	// Older runtime paths can still send directly to the event channel. Drain
	// after cancellation so a failed network write cannot strand that producer.
	defer func() {
		cancel()
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for range events {
			}
		}()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-drained:
		case <-timer.C:
			slog.Warn("gRPC chat cleanup exceeded deadline", "session_id", sess.ID)
		}
	}()
	var sent strings.Builder
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case event, ok := <-events:
			if !ok {
				if ctx.Err() != nil {
					return status.FromContextError(ctx.Err()).Err()
				}
				return status.Error(codes.Internal, "chat stream ended without completion")
			}
			switch event.Type {
			case agent.ChatEventContent:
				if event.Content == "" {
					continue
				}
				if err := stream.Send(&ChatChunk{Content: event.Content, SessionId: sess.ID}); err != nil {
					return chatStatusError(err)
				}
				sent.WriteString(event.Content)
			case agent.ChatEventDone:
				// Content frames are deltas. The runtime's done event carries the
				// entire answer, so only send a missing suffix, never duplicate it.
				content := ""
				if strings.HasPrefix(event.Content, sent.String()) {
					content = strings.TrimPrefix(event.Content, sent.String())
				}
				return stream.Send(&ChatChunk{Content: content, SessionId: sess.ID, Done: true})
			case agent.ChatEventError:
				if event.Err == nil {
					return status.Error(codes.Internal, "chat failed")
				}
				return chatStatusError(event.Err)
			}
		}
	}
}

// prepareChat applies the same session and loop policy to both gRPC methods.
// proto3's bool has no presence bit: false (including omission) disables
// automatic approval. Like HTTP, an authenticated owner can explicitly enable
// it for this turn; authentication is enforced by GRPCServer's interceptors.
func (s *Server) prepareChat(ctx context.Context, req *ChatRequest) (*session.Session, agent.LoopConfig, error) {
	loopCfg := agent.DefaultLoopConfig()
	if err := ctx.Err(); err != nil {
		return nil, loopCfg, status.FromContextError(err).Err()
	}
	if req == nil || strings.TrimSpace(req.Message) == "" {
		return nil, loopCfg, status.Error(codes.InvalidArgument, "message is required")
	}
	if req.MaxIterations < 0 || req.MaxIterations > 300 {
		return nil, loopCfg, status.Error(codes.InvalidArgument, "max_iterations must be between 0 and 300")
	}
	if len(req.Metadata) != 0 {
		return nil, loopCfg, status.Error(codes.InvalidArgument, "chat metadata is not supported")
	}
	if s == nil || s.agent == nil || s.agent.Sessions() == nil {
		return nil, loopCfg, status.Error(codes.Unavailable, "agent is not initialized")
	}
	if mgr := s.agent.Config(); mgr != nil {
		agent.ApplyAgentLoopConfig(&loopCfg, mgr.Get().Agent)
	}
	loopCfg.AutoApprove = req.AutoApprove
	if req.MaxIterations > 0 {
		loopCfg.MaxIterations = int(req.MaxIterations)
	}
	loopCfg.Source = "grpc"
	id := strings.TrimSpace(req.SessionId)
	if id != "" {
		sess, ok := s.agent.Sessions().Get(id)
		if !ok {
			return nil, loopCfg, status.Errorf(codes.NotFound, "session not found: %s", id)
		}
		return sess, loopCfg, nil
	}
	sess := s.agent.Sessions().NewWithTitle("grpc-chat")
	if sess == nil {
		return nil, loopCfg, status.Error(codes.Internal, "failed to create chat session")
	}
	return sess, loopCfg, nil
}

func chatStatusError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Errorf(codes.Internal, "chat failed: %v", err)
}

// MemoryStore stores a memory entry.
func (s *Server) MemoryStore(ctx context.Context, req *MemoryStoreRequest) (*MemoryEntry, error) {
	if s == nil || s.memoryStore == nil {
		return nil, status.Error(codes.Unavailable, "memory backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	tier := memory.TierMedium
	switch req.Tier {
	case "short":
		tier = memory.TierShort
	case "long":
		tier = memory.TierLong
	}

	result, err := memory.Write(ctx, s.memoryStore, memory.RememberRequest{
		Content: req.Content, Category: req.Category, Tier: tier, Importance: req.Importance,
	})
	if err != nil {
		code := codes.Internal
		var rejected *memory.WriteRejectedError
		if errors.As(err, &rejected) {
			code = codes.InvalidArgument
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, status.FromContextError(err).Err()
		}
		return nil, status.Errorf(code, "failed to store memory: %v", err)
	}

	// Return this write's ID, including duplicate updates and concurrent calls.
	entry, err := s.memoryStore.Get(result.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to retrieve stored memory")
	}

	return &MemoryEntry{
		Id:          entry.ID,
		Content:     entry.Content,
		Category:    entry.Category,
		Tier:        entry.Tier.String(),
		Importance:  entry.Importance,
		AccessCount: int32(entry.AccessCount),
		CreatedAt:   timestamppb.New(entry.CreatedAt),
		UpdatedAt:   timestamppb.New(entry.CreatedAt),
	}, nil
}

// MemoryRecall recalls memories matching query.
func (s *Server) MemoryRecall(ctx context.Context, req *MemoryRecallRequest) (*MemoryRecallResponse, error) {
	if s == nil || s.memoryStore == nil {
		return nil, status.Error(codes.Unavailable, "memory backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	results, err := s.memoryStore.Recall(ctx, memory.RecallRequest{Query: req.Query, Options: memory.SearchOptions{
		Limit: int(req.Limit), IncludeGraph: true, GraphDepth: 1,
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, status.FromContextError(err).Err()
		}
		return nil, status.Errorf(codes.Internal, "failed to recall memory: %v", err)
	}

	pbEntries := make([]*MemoryEntry, len(results))
	for i, result := range results {
		e := result.Entry
		pbEntries[i] = &MemoryEntry{
			Id:          e.ID,
			Content:     e.Content,
			Category:    e.Category,
			Tier:        e.Tier.String(),
			Importance:  e.Importance,
			AccessCount: int32(e.AccessCount),
			CreatedAt:   timestamppb.New(e.CreatedAt),
			UpdatedAt:   timestamppb.New(e.CreatedAt),
		}
	}

	return &MemoryRecallResponse{
		Entries: pbEntries,
		Total:   int32(len(pbEntries)),
	}, nil
}

// MemoryList lists all memory entries.
func (s *Server) MemoryList(ctx context.Context, _ *emptypb.Empty) (*MemoryListResponse, error) {
	if s == nil || s.memoryStore == nil {
		return nil, status.Error(codes.Unavailable, "memory backend is not initialized")
	}
	entries := s.memoryStore.Recent(s.memoryStore.Count())

	pbEntries := make([]*MemoryEntry, len(entries))
	for i, e := range entries {
		pbEntries[i] = &MemoryEntry{
			Id:          e.ID,
			Content:     e.Content,
			Category:    e.Category,
			Tier:        e.Tier.String(),
			Importance:  e.Importance,
			AccessCount: int32(e.AccessCount),
			CreatedAt:   timestamppb.New(e.CreatedAt),
			UpdatedAt:   timestamppb.New(e.CreatedAt),
		}
	}

	return &MemoryListResponse{
		Entries: pbEntries,
		Count:   int32(len(pbEntries)),
	}, nil
}

// MemoryDelete deletes a memory entry.
func (s *Server) MemoryDelete(ctx context.Context, req *MemoryDeleteRequest) (*emptypb.Empty, error) {
	if s == nil || s.memoryStore == nil {
		return nil, status.Error(codes.Unavailable, "memory backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if err := s.memoryStore.Delete(req.Id); err != nil {
		return nil, status.Errorf(codes.NotFound, "memory entry not found: %s", req.Id)
	}
	return &emptypb.Empty{}, nil
}

// RAGIndex indexes content for RAG.
func (s *Server) RAGIndex(ctx context.Context, req *RAGIndexRequest) (*RAGIndexResponse, error) {
	if s == nil || s.ragManager == nil {
		return nil, status.Error(codes.Unavailable, "RAG backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	doc, err := s.ragManager.IndexText(req.Source, "", req.Content)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to index content: %v", err)
	}

	stats := s.ragManager.Stats()
	return &RAGIndexResponse{
		Id:           doc.ID,
		Dimension:    1536, // OpenAI embedding dimension
		TotalEntries: int32(stats.ChunkCount),
	}, nil
}

// RAGSearch searches indexed content.
func (s *Server) RAGSearch(ctx context.Context, req *RAGSearchRequest) (*RAGSearchResponse, error) {
	if s == nil || s.ragManager == nil {
		return nil, status.Error(codes.Unavailable, "RAG backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 10
	}

	threshold := req.Threshold
	if threshold <= 0 {
		threshold = 0.7
	}

	results, err := s.ragManager.Search(ctx, req.Query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "search failed: %v", err)
	}

	pbResults := make([]*RAGResult, 0, len(results))
	for _, r := range results {
		if r.Score < threshold {
			continue
		}

		pbResults = append(pbResults, &RAGResult{
			Id:       r.ChunkID,
			Content:  r.Content,
			Source:   r.DocSource,
			Score:    r.Score,
			Metadata: r.Metadata,
		})

		if len(pbResults) >= limit {
			break
		}
	}

	return &RAGSearchResponse{
		Results: pbResults,
		Total:   int32(len(pbResults)),
	}, nil
}

// WorkflowCreate creates a new workflow.
func (s *Server) WorkflowCreate(ctx context.Context, req *WorkflowCreateRequest) (*Workflow, error) {
	if s == nil || s.workflowEngine == nil {
		return nil, status.Error(codes.Unavailable, "workflow backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	tasks := make([]*workflow.Task, len(req.Tasks))
	for i, t := range req.Tasks {
		if t == nil {
			return nil, status.Error(codes.InvalidArgument, "workflow tasks must not contain null entries")
		}
		params := make(map[string]interface{})
		for k, v := range t.Params {
			params[k] = v
		}

		tasks[i] = &workflow.Task{
			ID:          t.Id,
			Name:        t.Name,
			Description: t.Description,
			Action:      t.Action,
			Params:      params,
			DependsOn:   t.DependsOn,
			Timeout:     time.Duration(t.TimeoutMs) * time.Millisecond,
			RetryCount:  int(t.RetryCount),
			RetryDelay:  time.Duration(t.RetryDelayMs) * time.Millisecond,
		}
	}

	wf := workflow.NewWorkflow(req.Name, tasks)
	wf.Description = req.Description
	wf.Version = req.Version

	if err := s.workflowEngine.RegisterWorkflow(wf); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid workflow: %v", err)
	}

	return s.workflowToProto(wf), nil
}

// WorkflowGet gets a workflow by ID.
func (s *Server) WorkflowGet(ctx context.Context, req *WorkflowGetRequest) (*Workflow, error) {
	if s == nil || s.workflowEngine == nil {
		return nil, status.Error(codes.Unavailable, "workflow backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	wf, ok := s.workflowEngine.GetWorkflow(req.Id)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "workflow not found: %s", req.Id)
	}
	return s.workflowToProto(wf), nil
}

// WorkflowList lists all workflows.
func (s *Server) WorkflowList(ctx context.Context, _ *emptypb.Empty) (*WorkflowListResponse, error) {
	if s == nil || s.workflowEngine == nil {
		return nil, status.Error(codes.Unavailable, "workflow backend is not initialized")
	}
	workflows := s.workflowEngine.ListWorkflows()

	pbWorkflows := make([]*Workflow, len(workflows))
	for i, wf := range workflows {
		pbWorkflows[i] = s.workflowToProto(wf)
	}

	return &WorkflowListResponse{
		Workflows: pbWorkflows,
		Count:     int32(len(pbWorkflows)),
	}, nil
}

// WorkflowDelete deletes a workflow.
func (s *Server) WorkflowDelete(ctx context.Context, req *WorkflowDeleteRequest) (*emptypb.Empty, error) {
	if s == nil || s.workflowEngine == nil {
		return nil, status.Error(codes.Unavailable, "workflow backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if err := s.workflowEngine.DeleteWorkflow(req.Id); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete workflow: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// WorkflowStart starts a workflow instance.
func (s *Server) WorkflowStart(ctx context.Context, req *WorkflowStartRequest) (*WorkflowInstance, error) {
	if s == nil || s.workflowEngine == nil {
		return nil, status.Error(codes.Unavailable, "workflow backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	instance, err := s.workflowEngine.StartWorkflow(req.WorkflowId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to start workflow: %v", err)
	}

	return s.instanceToProto(instance), nil
}

// WorkflowInstanceGet gets a workflow instance.
func (s *Server) WorkflowInstanceGet(ctx context.Context, req *WorkflowInstanceGetRequest) (*WorkflowInstance, error) {
	if s == nil || s.workflowEngine == nil {
		return nil, status.Error(codes.Unavailable, "workflow backend is not initialized")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	instance, ok := s.workflowEngine.GetInstance(req.Id)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "instance not found: %s", req.Id)
	}
	return s.instanceToProto(instance), nil
}

// HealthCheck returns server health status.
func (s *Server) HealthCheck(ctx context.Context, _ *emptypb.Empty) (*HealthCheckResponse, error) {
	if s == nil {
		return nil, status.Error(codes.Unavailable, "service is not initialized")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	components := make(map[string]*ComponentHealth)

	// Check agent
	if s.agent != nil {
		components["agent"] = &ComponentHealth{
			Status:  "healthy",
			Message: "running",
		}
	}

	// Check memory
	if s.memoryStore != nil {
		components["memory"] = &ComponentHealth{
			Status:  "healthy",
			Message: fmt.Sprintf("%d entries", s.memoryStore.Count()),
		}
	}

	// Check RAG
	if s.ragManager != nil {
		stats := s.ragManager.Stats()
		components["rag"] = &ComponentHealth{
			Status:  "healthy",
			Message: fmt.Sprintf("%d chunks, %d documents", stats.ChunkCount, stats.DocumentCount),
		}
	}

	// Check workflow
	if s.workflowEngine != nil {
		workflows := s.workflowEngine.ListWorkflows()
		components["workflow"] = &ComponentHealth{
			Status:  "healthy",
			Message: fmt.Sprintf("%d workflows", len(workflows)),
		}
	}

	healthStatus := "healthy"
	if len(components) == 0 {
		healthStatus = "degraded"
	}
	return &HealthCheckResponse{
		Status:     healthStatus,
		Version:    "v0.25.0",
		Uptime:     timestamppb.New(s.startAt),
		Components: components,
	}, nil
}

// Helper functions

func (s *Server) workflowToProto(wf *workflow.Workflow) *Workflow {
	tasks := make([]*Task, len(wf.Tasks))
	for i, t := range wf.Tasks {
		params := make(map[string]string)
		for k, v := range t.Params {
			params[k] = fmt.Sprintf("%v", v)
		}

		tasks[i] = &Task{
			Id:           t.ID,
			Name:         t.Name,
			Description:  t.Description,
			Action:       t.Action,
			Params:       params,
			DependsOn:    t.DependsOn,
			TimeoutMs:    int64(t.Timeout.Milliseconds()),
			RetryCount:   int32(t.RetryCount),
			RetryDelayMs: int64(t.RetryDelay.Milliseconds()),
		}
	}

	return &Workflow{
		Id:          wf.ID,
		Name:        wf.Name,
		Description: wf.Description,
		Tasks:       tasks,
		Version:     wf.Version,
		CreatedAt:   timestamppb.New(wf.CreatedAt),
		UpdatedAt:   timestamppb.New(wf.UpdatedAt),
	}
}

func (s *Server) instanceToProto(instance *workflow.WorkflowInstance) *WorkflowInstance {
	// Use Snapshot() for thread-safe access
	snap := instance.Snapshot()

	results := make(map[string]*TaskResult)
	for k, r := range snap.Results {
		results[k] = &TaskResult{
			TaskId:     r.TaskID,
			Status:     string(r.Status),
			Output:     fmt.Sprintf("%v", r.Output),
			Error:      r.Error,
			StartTime:  timestamppb.New(r.StartTime),
			EndTime:    timestamppb.New(r.EndTime),
			DurationMs: r.Duration.Milliseconds(),
		}
	}

	return &WorkflowInstance{
		Id:         snap.ID,
		WorkflowId: snap.WorkflowID,
		Status:     string(snap.Status),
		Results:    results,
		StartTime:  timestamppb.New(snap.StartTime),
		EndTime:    timestamppb.New(snap.EndTime),
	}
}

// GRPCServer wraps the gRPC server with additional functionality.
type GRPCServer struct {
	server           *grpc.Server
	health           *health.Server
	listener         net.Listener
	addr             string
	limiter          *transport.Limiter
	admissionLimiter *transport.Limiter
}

// GRPCServerOptions optionally supplies transport policy for hosts that do not
// have an Agent configuration. A nil Config inherits the Agent's server keys
// and rate limit, or uses local-only access with the default rate limit.
type GRPCServerOptions struct {
	Config          *transport.Config
	PairingKeyValid func(string) bool
}

// NewGRPCServer creates a gRPC server using the Agent's server access policy.
func NewGRPCServer(addr string, serviceServer *Server) *GRPCServer {
	return NewGRPCServerWithOptions(addr, serviceServer, GRPCServerOptions{})
}

// NewGRPCServerWithOptions creates a server with an explicit access policy.
func NewGRPCServerWithOptions(addr string, serviceServer *Server, options GRPCServerOptions) *GRPCServer {
	cfg := transport.DefaultConfig()
	if serviceServer != nil && serviceServer.agent != nil && serviceServer.agent.Config() != nil {
		serverCfg := serviceServer.agent.Config().Get().Server
		cfg.APIKeys = append([]string(nil), serverCfg.APIKeys...)
		cfg.RateLimit = serverCfg.RateLimit
	}
	if options.Config != nil {
		cfg = *options.Config
		cfg.APIKeys = append([]string(nil), cfg.APIKeys...)
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = transport.DefaultConfig().RateLimit
	}
	limiter := transport.NewLimiter(cfg.RateLimit)
	admissionLimiter := transport.NewLimiter(grpcAdmissionLimit(cfg.RateLimit))
	policy := grpcAccessPolicy{config: cfg, limiter: limiter, admissionLimiter: admissionLimiter, pairingKeyValid: options.PairingKeyValid}
	server := grpc.NewServer(grpc.UnaryInterceptor(policy.unary), grpc.StreamInterceptor(policy.stream))

	// Register service
	RegisterLuckyAgentServiceServer(server, serviceServer)

	// Register health service
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("luckyagent", grpc_health_v1.HealthCheckResponse_SERVING)

	// Register reflection
	reflection.Register(server)

	return &GRPCServer{
		server:           server,
		health:           healthServer,
		addr:             addr,
		limiter:          limiter,
		admissionLimiter: admissionLimiter,
	}
}

type grpcAccessPolicy struct {
	config           transport.Config
	limiter          *transport.Limiter
	admissionLimiter *transport.Limiter
	pairingKeyValid  func(string) bool
}

func (p *grpcAccessPolicy) admit(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	peerInfo, hasPeer := peer.FromContext(ctx)
	ip := ""
	if hasPeer && peerInfo.Addr != nil {
		ip = transport.RemoteIP(peerInfo.Addr.String())
	}
	// Authentication failures must also consume a bounded peer budget. This
	// separate, larger admission quota does not consume owner business quota.
	if !p.admissionLimiter.Allow("grpc-admission:" + ip) {
		return status.Error(codes.ResourceExhausted, "request admission rate exceeded")
	}
	key := firstMetadata(md, "x-api-key")
	if key == "" {
		key = firstMetadata(md, "authorization")
		key = strings.TrimPrefix(key, "Bearer ")
	}
	valid := key != "" && p.pairingKeyValid != nil && p.pairingKeyValid(key)
	for _, allowed := range p.config.APIKeys {
		if key != "" && allowed != "" && subtle.ConstantTimeCompare([]byte(allowed), []byte(key)) == 1 {
			valid = true
		}
	}
	bucket := ip
	if !valid {
		if len(p.config.APIKeys) > 0 || !grpcLocalPeer(ip, md) {
			if key == "" {
				return status.Error(codes.Unauthenticated, "api key required")
			}
			return status.Error(codes.PermissionDenied, "invalid api key")
		}
	} else {
		// Hash the credential so limiter state never retains a usable secret.
		bucket = transport.CredentialBucketKey(key)
	}
	if !p.limiter.Allow(bucket) {
		return status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
	return nil
}

func grpcAdmissionLimit(businessLimit int) int {
	maxInt := int(^uint(0) >> 1)
	if businessLimit > maxInt/10 {
		return maxInt
	}
	if businessLimit < 6 {
		return 60
	}
	return businessLimit * 10
}

func firstMetadata(md metadata.MD, key string) string {
	values := md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func grpcLocalPeer(ip string, md metadata.MD) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil || !parsed.IsLoopback() {
		return false
	}
	// A loopback reverse proxy must authenticate its remote callers too.
	for _, header := range []string{"forwarded", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto", "x-real-ip"} {
		if len(md.Get(header)) != 0 {
			return false
		}
	}
	return true
}

func (p *grpcAccessPolicy) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
	defer func() {
		if recover() != nil {
			slog.Error("gRPC unary handler panic recovered")
			response, err = nil, status.Error(codes.Internal, "internal server error")
		}
	}()
	if err := p.admit(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (p *grpcAccessPolicy) stream(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if recover() != nil {
			slog.Error("gRPC stream handler panic recovered")
			err = status.Error(codes.Internal, "internal server error")
		}
	}()
	if err := p.admit(stream.Context()); err != nil {
		return err
	}
	return handler(srv, stream)
}

// Start starts the gRPC server.
func (s *GRPCServer) Start() error {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}
	s.listener = listener

	go func() {
		s.server.Serve(listener)
	}()

	return nil
}

// Stop stops the gRPC server gracefully.
func (s *GRPCServer) Stop() {
	defer s.limiter.Stop()
	defer s.admissionLimiter.Stop()
	s.health.SetServingStatus("luckyagent", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	s.server.GracefulStop()
}

// Addr returns the server address.
func (s *GRPCServer) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}
