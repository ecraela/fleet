package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/ecraela/fleet/internal/appprobe"
	"github.com/ecraela/fleet/internal/buildinfo"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/ecraela/fleet/internal/controlv1"
	"github.com/ecraela/fleet/internal/controlwire"
	"github.com/ecraela/fleet/internal/dataplane"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/haproxyruntime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

type journalEntry struct {
	Operation    domain.Operation        `json:"operation"`
	Digest       string                  `json:"digest"`
	Result       *domain.OperationResult `json:"result"`
	Acknowledged bool                    `json:"acknowledged"`
}
type operationJournal struct {
	mu      sync.Mutex
	path    string
	entries []journalEntry
}

func loadJournal(path string) (*operationJournal, error) {
	j := &operationJournal{path: path, entries: []journalEntry{}}
	raw, err := readPrivate(path, 8<<20)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &j.entries) != nil || len(j.entries) > 64 {
		return nil, errors.New("invalid operation journal")
	}
	for _, entry := range j.entries {
		if !domain.ValidID(entry.Operation.ID) || entry.Digest != entry.Operation.Digest() {
			return nil, errors.New("invalid operation journal")
		}
	}
	return j, nil
}
func (j *operationJournal) begin(op domain.Operation) (journalEntry, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, entry := range j.entries {
		if entry.Operation.ID == op.ID {
			if entry.Digest != op.Digest() {
				return entry, false, domain.ErrConflict
			}
			return entry, false, nil
		}
	}
	for len(j.entries) >= 64 && j.entries[0].Acknowledged {
		j.entries = j.entries[1:]
	}
	if len(j.entries) >= 64 {
		return journalEntry{}, false, errors.New("journal full")
	}
	entry := journalEntry{Operation: op, Digest: op.Digest()}
	j.entries = append(j.entries, entry)
	return entry, true, saveJSON(j.path, j.entries)
}
func (j *operationJournal) complete(id string, result domain.OperationResult) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range j.entries {
		if j.entries[i].Operation.ID == id {
			if result.Discovery != nil && j.entries[i].Operation.Kind != "configuration" {
				snapshot := *result.Discovery
				snapshot.Proxies = []domain.Proxy{}
				snapshot.Servers = []domain.BackendServer{}
				op := j.entries[i].Operation
				if server, ok := result.Discovery.Server(op.Backend, op.Server); ok {
					snapshot.Servers = append(snapshot.Servers, server)
				}
				result.Discovery = &snapshot
			}
			j.entries[i].Result = &result
			j.entries[i].Acknowledged = false
			return saveJSON(j.path, j.entries)
		}
	}
	return domain.ErrNotFound
}
func (j *operationJournal) pending() *domain.OperationResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, entry := range j.entries {
		if !entry.Acknowledged && entry.Result != nil {
			copy := *entry.Result
			return &copy
		}
	}
	return nil
}
func (j *operationJournal) acknowledge(id string) error {
	if id == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range j.entries {
		if j.entries[i].Operation.ID == id && j.entries[i].Result != nil {
			j.entries[i].Acknowledged = true
			return saveJSON(j.path, j.entries)
		}
	}
	return domain.ErrConflict
}
func (j *operationJournal) recover(ctx context.Context, runtime haproxyruntime.Client, config ...*dataplane.Client) error {
	for _, entry := range j.entries {
		if entry.Acknowledged || (entry.Result != nil && entry.Result.Outcome == "failed") {
			continue
		}
		recovery, cancel := context.WithTimeout(ctx, 3*time.Second)
		execute := runtime.Execute
		if entry.Operation.Kind == "configuration" {
			if len(config) == 0 || config[0] == nil {
				cancel()
				return errors.New("configuration recovery requires its local adapter")
			}
			execute = config[0].Execute
		}
		d, outcome, code := execute(recovery, entry.Operation, false)
		cancel()
		if err := j.complete(entry.Operation.ID, domain.OperationResult{OperationID: entry.Operation.ID, Outcome: outcome, ErrorCode: code, Discovery: &d, CommandDigest: entry.Digest}); err != nil {
			return err
		}
	}
	return nil
}

// Control has its own TLS connection, timeouts and journal. Slow discovery or
// management must never block the telemetry collector or HAProxy's serving path.
func controlLoop(ctx context.Context, c Config, trust *x509.CertPool) {
	for ctx.Err() == nil {
		_ = controlSession(ctx, c, trust)
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func controlSession(parent context.Context, c Config, trust *x509.CertPool) error {
	state, err := readState(filepath.Join(c.StateDir, "identity.json"))
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair([]byte(state.CertificatePEM), []byte(state.KeyPEM))
	if err != nil {
		return err
	}
	roots := trust.Clone()
	if !roots.AppendCertsFromPEM([]byte(state.CAPEM)) {
		return errors.New("invalid node trust")
	}
	journal, err := loadJournal(filepath.Join(c.StateDir, "operations.json"))
	if err != nil {
		return err
	}
	runtime := haproxyruntime.Client{Socket: c.RuntimeSocket, ConfigFiles: c.ConfigFiles, AllowControl: c.AllowRuntimeControl}
	var configuration *dataplane.Client
	if c.DataplaneSocket != "" {
		configuration = configurationClient(c, runtime, c.AllowConfigurationControl)
	}
	if configuration != nil {
		defer configuration.Close()
		configuration.NodeID = state.Node.ID
		configuration.AllowRequestLogs = c.AccessLogSocket != ""
		configuration.Traffic = c.traffic
		if c.issuer != nil {
			configuration.ACMEGrants = c.issuer.Grants
			configuration.DynamicCertificates = func() []dataplane.LocalCertificate {
				out := []dataplane.LocalCertificate{}
				for _, a := range c.issuer.Assets() {
					out = append(out, dataplane.LocalCertificate{Reference: a.Reference, Path: a.Path})
				}
				return out
			}
		}

		if c.TLSPolicyFile != "" {
			if err = configuration.LoadTLSPolicy(c.TLSPolicyFile); err != nil {
				configuration.Close()
				configuration = nil
			}
		}
	}
	if err = journal.recover(parent, runtime, configuration); err != nil {
		return err
	}
	connection, err := grpc.NewClient(strings.TrimPrefix(c.ServerURL, "https://"),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{pair}})),
		grpc.WithDisableRetry(), grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: time.Second, Multiplier: 1.5, Jitter: 0.2, MaxDelay: 3 * time.Second}, MinConnectTimeout: 3 * time.Second}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(128<<10), grpc.MaxCallSendMsgSize(256<<10)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 10 * time.Second, Timeout: 3 * time.Second}))
	if err != nil {
		return err
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(parent, 14*time.Minute)
	defer cancel()
	var probes atomic.Pointer[appprobe.Runner]
	if c.ProbePolicyFile != "" {
		go func() {
			runner, loadErr := appprobe.Load(ctx, c.ProbePolicyFile)
			if loadErr != nil {
				log.Print("application probe policy unavailable; checks disabled for this control session")
				return
			}
			probes.Store(runner)
			runner.Run(ctx)
		}()
	}
	stream, err := pb.NewControlClient(connection).Connect(ctx)
	if err != nil {
		return err
	}
	var workerDone chan struct{}
	var cancelWorker context.CancelFunc
	runningID := ""
	defer func() {
		if cancelWorker != nil {
			cancelWorker()
		}
		if workerDone != nil {
			<-workerDone
		}
	}()
	session := ""
	for ctx.Err() == nil {
		if workerDone != nil {
			select {
			case <-workerDone:
				workerDone = nil
				runningID = ""
				cancelWorker = nil
			default:
			}
		}
		discoveryCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		d := runtime.Discover(discoveryCtx)
		files, automated := inspectFiles(c.ConfigFiles)
		admin := c.AllowRuntimeControl && d.ErrorCode == "" && runtime.AdminAccess(discoveryCtx)
		d.ControlAllowed = d.ControlAllowed && admin
		build := buildinfo.Current()
		d.Agent = &build
		if configuration != nil {
			d.Configuration = configuration.Observe(discoveryCtx)
			d.Configuration.ControlAllowed = d.Configuration.ControlAllowed && configurationOwnershipAllows(c, files, automated)
		}
		var metrics *metricsObservation
		if c.metricsHealth != nil {
			metrics = c.metricsHealth.Load()
		}
		d.Setup = nodeSetup(c, d, metrics, files, automated, admin)
		stop()
		if runner := probes.Load(); runner != nil {
			if d.Configuration != nil && d.Configuration.Document != nil && d.Configuration.Fresh(time.Now()) {
				runner.Update(&d)
			} else {
				runner.Update(nil)
			}
			d.ApplicationProbes = runner.Snapshot()
			d.ApplicationProbeGrants = runner.Grants()
		}
		result := journal.pending()
		deadline := time.AfterFunc(6*time.Second, cancel)
		err = stream.Send(&pb.AgentReport{SessionId: session, Discovery: controlwire.Discovery(&d), Result: controlwire.Result(result)})
		var frame *pb.ManagerFrame
		if err == nil {
			frame, err = stream.Recv()
		}
		deadline.Stop()
		if err != nil {
			return err
		}
		if !domain.ValidID(frame.SessionId) || (session != "" && frame.SessionId != session) {
			return errors.New("control session changed")
		}
		session = frame.SessionId
		if frame.AckId != "" {
			if result == nil || frame.AckId != result.OperationID {
				return errors.New("unexpected operation acknowledgment")
			}
			if err = journal.acknowledge(frame.AckId); err != nil {
				return err
			}
		}
		if !frame.ManagementEnabled && cancelWorker != nil {
			cancelWorker()
		}
		if frame.Command != nil {
			if workerDone != nil {
				select {
				case <-workerDone:
					workerDone = nil
					runningID = ""
					cancelWorker = nil
				default:
				}
			}
			op := controlwire.ReadCommand(frame.Command)
			if !frame.ManagementEnabled || (op.Kind == "configuration" && (configuration == nil || !c.AllowConfigurationControl || !configurationOwnershipAllows(c, files, automated))) || (op.Kind != "configuration" && (!c.AllowRuntimeControl || !admin)) || !validControlCommand(op) {
				return errors.New("invalid runtime command")
			}
			entry, fresh, err := journal.begin(op)
			if err != nil {
				return err
			}
			if workerDone != nil {
				if runningID != op.ID {
					return errors.New("concurrent runtime command")
				}
			} else if entry.Result != nil {
				if err = journal.complete(op.ID, *entry.Result); err != nil {
					return err
				}
			} else {
				operationCtx, operationCancel := context.WithDeadline(ctx, op.Deadline)
				cancelWorker = operationCancel
				workerDone = make(chan struct{})
				done := workerDone
				runningID = op.ID
				go func() {
					defer close(done)
					defer operationCancel()
					execute := runtime.Execute
					if op.Kind == "configuration" {
						execute = configuration.Execute
					}
					found, outcome, code := execute(operationCtx, op, fresh)
					_ = journal.complete(op.ID, domain.OperationResult{OperationID: op.ID, Outcome: outcome, ErrorCode: code, Discovery: &found, CommandDigest: op.Digest()})
				}()
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
func validControlCommand(op domain.Operation) bool {
	if op.Kind == "configuration" {
		copy := op
		copy.NodeID = domain.NewID()
		copy.RequestID = domain.NewID()
		return domain.ValidID(op.ID) && copy.Valid() && op.Deadline.After(time.Now()) && op.Deadline.Before(time.Now().Add(65*time.Second))
	}

	return domain.ValidID(op.ID) && domain.ValidDigest(op.ExpectedGeneration) && domain.ValidRuntimeName(op.Backend) && domain.ValidRuntimeName(op.Server) && op.Weight <= 256 && (op.Kind == "maintenance" || op.Kind == "drain" || op.Kind == "restore" || op.Kind == "weight") && (op.ExpectedState == "ready" || op.ExpectedState == "drain" || op.ExpectedState == "maint") && op.ExpectedWeight <= 256 && op.Deadline.After(time.Now()) && op.Deadline.Before(time.Now().Add(35*time.Second))
}
