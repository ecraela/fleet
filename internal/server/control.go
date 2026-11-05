package server

import (
	"context"
	"errors"
	"github.com/ecraela/fleet/internal/buildinfo"
	"io"
	"net/http"
	"strings"
	"time"

	pb "github.com/ecraela/fleet/internal/controlv1"
	"github.com/ecraela/fleet/internal/controlwire"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type controlNodeKey struct{}
type nodeControlService struct {
	pb.UnimplementedControlServer
	server *Server
}

func (s *Server) configureControl() {
	s.configureConfiguration()
	s.controlRPC = grpc.NewServer(grpc.MaxRecvMsgSize(256<<10), grpc.MaxSendMsgSize(128<<10), grpc.MaxConcurrentStreams(64))
	pb.RegisterControlServer(s.controlRPC, &nodeControlService{server: s})
	s.controls = make(chan struct{}, 64)
	s.controlReports = &nodeReportBudget{windows: make(map[string]nodeReportWindow)}
	s.mux.HandleFunc("GET /v1/nodes/{node}/discovery", s.authorizeCapability("haproxy.read", s.nodeDiscovery))
	s.mux.HandleFunc("POST /v1/nodes/{node}/operations", s.authorizeCapability("haproxy.manage", s.createRuntimeOperation))
}
func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor != 2 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		fail(w, 400, "gRPC over TLS required")
		return
	}
	node, err := s.authenticateNode(r)
	if err != nil {
		fail(w, 401, "valid node certificate required")
		return
	}
	if !acquire(s.controls) {
		fail(w, 429, "control capacity reached")
		return
	}
	defer func() { <-s.controls }()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	s.controlRPC.ServeHTTP(w, r.WithContext(context.WithValue(ctx, controlNodeKey{}, node)))
}
func (service *nodeControlService) Connect(stream grpc.BidiStreamingServer[pb.AgentReport, pb.ManagerFrame]) error {
	s := service.server
	node, ok := stream.Context().Value(controlNodeKey{}).(domain.Node)
	if !ok {
		return status.Error(codes.Unauthenticated, "node certificate required")
	}
	persistence, ok := s.config.Store.(store.ControlStore)
	if !ok {
		return status.Error(codes.Unavailable, "control storage unavailable")
	}
	if !s.controlReports.admit(node.ID, time.Now()) {
		return status.Error(codes.ResourceExhausted, "node report rate exceeded")
	}
	session := domain.NewID()
	ctx, cancel := context.WithTimeout(stream.Context(), 6*time.Second)
	_, err := persistence.OpenControl(ctx, node.OrganizationID, node.ID, session)
	cancel()
	if err != nil {
		return status.Error(codes.PermissionDenied, "node unavailable")
	}
	// A bounded receive channel and timeout stop dead peers retaining authority.
	type received struct {
		report *pb.AgentReport
		err    error
	}
	inbox := make(chan received, 1)
	go func() {
		for {
			report, err := stream.Recv()
			select {
			case inbox <- received{report, err}:
			case <-stream.Context().Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		var report *pb.AgentReport
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-time.After(6 * time.Second):
			return status.Error(codes.DeadlineExceeded, "heartbeat overdue")
		case item := <-inbox:
			if item.err != nil {
				if errors.Is(item.err, io.EOF) {
					return nil
				}
				return item.err
			}
			report = item.report
		}
		if report == nil || (report.SessionId != "" && report.SessionId != session) {
			return status.Error(codes.FailedPrecondition, "control session changed")
		}
		if !s.controlReports.admit(node.ID, time.Now()) {
			return status.Error(codes.ResourceExhausted, "node report rate exceeded")
		}
		ctx, cancel := context.WithTimeout(stream.Context(), 3*time.Second)
		state, command, err := persistence.ReportControl(ctx, node.OrganizationID, node.ID, session, controlwire.ReadDiscovery(report.Discovery), controlwire.ReadResult(report.Result))
		cancel()
		if err != nil {
			return status.Error(codes.FailedPrecondition, "control authority changed")
		}
		frame := &pb.ManagerFrame{SessionId: session, Command: controlwire.Command(command), ManagementEnabled: state.Discovery != nil && state.Discovery.Fresh(time.Now()) && (state.Discovery.ControlAllowed || (state.Discovery.Configuration != nil && state.Discovery.Configuration.Fresh(time.Now()) && state.Discovery.Configuration.ControlAllowed))}
		if report.Result != nil {
			frame.AckId = report.Result.OperationId
		}
		// Returning ends the RPC and cancels any blocked HTTP/2 write.
		sent := make(chan error, 1)
		go func() { sent <- stream.Send(frame) }()
		select {
		case err := <-sent:
			if err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-time.After(3 * time.Second):
			return status.Error(codes.DeadlineExceeded, "control receiver stalled")
		}
	}
}
func (s *Server) nodeDiscovery(w http.ResponseWriter, r *http.Request, org string) {
	persistence, ok := s.config.Store.(store.ControlStore)
	if !ok {
		fail(w, 503, "discovery unavailable")
		return
	}
	state, operations, err := persistence.GetControl(r.Context(), org, r.PathValue("node"))
	if err != nil {
		storeError(w, err)
		return
	}
	connected := state.SessionID != "" && time.Since(state.LastSeen) <= 6*time.Second
	fresh := connected && state.Discovery != nil && state.Discovery.Fresh(time.Now())
	writeJSON(w, 200, map[string]any{"control": state, "connected": connected, "fresh": fresh, "management_enabled": fresh && state.Discovery.ControlAllowed, "operations": operations, "manager_build": buildinfo.Current()})
}
func (s *Server) createRuntimeOperation(w http.ResponseWriter, r *http.Request, org string) {
	var body struct {
		RequestID      string `json:"request_id"`
		Kind           string `json:"kind"`
		Backend        string `json:"backend"`
		Server         string `json:"server"`
		Weight         uint32 `json:"weight"`
		Generation     string `json:"expected_generation"`
		ExpectedState  string `json:"expected_state"`
		ExpectedWeight uint32 `json:"expected_weight"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	persistence, ok := s.config.Store.(store.ControlStore)
	if !ok {
		fail(w, 503, "control unavailable")
		return
	}
	op, err := persistence.CreateOperation(r.Context(), org, domain.Operation{RequestID: body.RequestID, NodeID: r.PathValue("node"), Kind: body.Kind, Backend: body.Backend, Server: body.Server, Weight: body.Weight, ExpectedGeneration: body.Generation, ExpectedState: body.ExpectedState, ExpectedWeight: body.ExpectedWeight})
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"operation": op})
}
