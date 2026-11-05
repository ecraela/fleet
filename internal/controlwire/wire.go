package controlwire

import (
	"github.com/ecraela/fleet/internal/buildinfo"
	"maps"
	"time"

	pb "github.com/ecraela/fleet/internal/controlv1"
	"github.com/ecraela/fleet/internal/domain"
)

func Discovery(d *domain.Discovery) *pb.Discovery {
	if d == nil {
		return nil
	}
	out := &pb.Discovery{Configuration: ConfigurationState(d.Configuration), Version: d.Version, ProcessId: d.ProcessID, StartedAt: d.StartedAt, ConfigFingerprint: d.ConfigFingerprint, Generation: d.Generation, ControlAllowed: d.ControlAllowed, ObservedAtMs: d.ObservedAtMS, ErrorCode: d.ErrorCode, ConfigFiles: d.ConfigFiles}
	if d.Setup != nil {
		out.Setup = &pb.NodeSetup{ObservedAtMs: d.Setup.ObservedAtMS, Owner: d.Setup.Owner}
		for _, c := range d.Setup.Checks {
			out.Setup.Checks = append(out.Setup.Checks, &pb.SetupCheck{Name: c.Name, Status: c.Status, Code: c.Code})
		}
	}
	if d.Agent != nil {
		a := d.Agent
		out.Agent = &pb.AgentBuild{Version: a.Version, Revision: a.Revision, Platform: a.Platform, Protocol: a.Protocol, ExecutableSha256: a.ExecutableSHA256}
	}
	for _, g := range d.ApplicationProbeGrants {
		out.ApplicationProbeGrants = append(out.ApplicationProbeGrants, &pb.ApplicationProbeGrant{Name: g.Name, Scope: g.Scope, Listener: g.Listener, Hostname: g.Hostname, PathPrefix: g.PathPrefix, Path: g.Path, Scheme: g.Scheme, ExpectedStatus: g.ExpectedStatus, PolicyDigest: g.PolicyDigest})
	}
	for _, p := range d.ApplicationProbes {
		out.ApplicationProbes = append(out.ApplicationProbes, &pb.ApplicationProbe{ApplicationId: p.ApplicationID, SpecDigest: p.SpecDigest, Name: p.Name, Scheme: p.Scheme, Path: p.Path, ExpectedStatus: p.ExpectedStatus, StatusCode: p.StatusCode, ObservedAtMs: p.ObservedAtMS, DurationMs: p.DurationMS, ErrorCode: p.ErrorCode, TlsVerified: p.TLSVerified, CertificateSha256: p.CertificateSHA256, CertificateNotAfterMs: p.CertificateNotAfterMS, Scope: p.Scope, PolicyDigest: p.PolicyDigest, ArtifactId: p.ArtifactID, Generation: p.Generation})
	}
	for _, p := range d.Proxies {
		out.Proxies = append(out.Proxies, &pb.Proxy{Name: p.Name, Kind: p.Kind, Mode: p.Mode, Algorithm: p.Algorithm, Status: p.Status, ActiveConnections: p.ActiveConnections})
	}
	for _, s := range d.Servers {
		out.Servers = append(out.Servers, &pb.BackendServer{Backend: s.Backend, Name: s.Name, Address: s.Address, State: s.State, Weight: s.Weight, InitialWeight: s.InitialWeight, ActiveConnections: s.ActiveConnections, Health: s.Health, CheckStatus: s.CheckStatus, AdminFlags: s.AdminFlags})
	}
	return out
}
func ReadDiscovery(d *pb.Discovery) *domain.Discovery {
	if d == nil {
		return nil
	}
	out := &domain.Discovery{Configuration: ReadConfigurationState(d.Configuration), Version: d.Version, ProcessID: d.ProcessId, StartedAt: d.StartedAt, ConfigFingerprint: d.ConfigFingerprint, Generation: d.Generation, ControlAllowed: d.ControlAllowed, ObservedAtMS: d.ObservedAtMs, ErrorCode: d.ErrorCode, ConfigFiles: d.ConfigFiles, Proxies: []domain.Proxy{}, Servers: []domain.BackendServer{}}
	if d.Setup != nil {
		out.Setup = &domain.NodeSetup{ObservedAtMS: d.Setup.ObservedAtMs, Owner: d.Setup.Owner}
		if len(d.Setup.Checks) != 6 {
			return nil
		}
		for _, c := range d.Setup.Checks {
			if c == nil {
				return nil
			}
			out.Setup.Checks = append(out.Setup.Checks, domain.SetupCheck{Name: c.Name, Status: c.Status, Code: c.Code})
		}
	}
	if d.Agent != nil {
		a := d.Agent
		out.Agent = &buildinfo.Info{Version: a.Version, Revision: a.Revision, Platform: a.Platform, Protocol: a.Protocol, ExecutableSHA256: a.ExecutableSha256}
	}
	for _, g := range d.ApplicationProbeGrants {
		if g == nil {
			return nil
		}
		out.ApplicationProbeGrants = append(out.ApplicationProbeGrants, domain.ApplicationProbeGrant{Name: g.Name, Scope: g.Scope, Listener: g.Listener, Hostname: g.Hostname, PathPrefix: g.PathPrefix, Path: g.Path, Scheme: g.Scheme, ExpectedStatus: g.ExpectedStatus, PolicyDigest: g.PolicyDigest})
	}
	for _, p := range d.ApplicationProbes {
		if p == nil {
			return nil
		}
		out.ApplicationProbes = append(out.ApplicationProbes, domain.ApplicationProbe{ApplicationID: p.ApplicationId, SpecDigest: p.SpecDigest, Name: p.Name, Scheme: p.Scheme, Path: p.Path, ExpectedStatus: p.ExpectedStatus, StatusCode: p.StatusCode, ObservedAtMS: p.ObservedAtMs, DurationMS: p.DurationMs, ErrorCode: p.ErrorCode, TLSVerified: p.TlsVerified, CertificateSHA256: p.CertificateSha256, CertificateNotAfterMS: p.CertificateNotAfterMs, Scope: p.Scope, PolicyDigest: p.PolicyDigest, ArtifactID: p.ArtifactId, Generation: p.Generation})
	}
	for _, p := range d.Proxies {
		if p == nil {
			return nil
		}
		out.Proxies = append(out.Proxies, domain.Proxy{Name: p.Name, Kind: p.Kind, Mode: p.Mode, Algorithm: p.Algorithm, Status: p.Status, ActiveConnections: p.ActiveConnections})
	}
	for _, s := range d.Servers {
		if s == nil {
			return nil
		}
		out.Servers = append(out.Servers, domain.BackendServer{Backend: s.Backend, Name: s.Name, Address: s.Address, State: s.State, Weight: s.Weight, InitialWeight: s.InitialWeight, ActiveConnections: s.ActiveConnections, Health: s.Health, CheckStatus: s.CheckStatus, AdminFlags: s.AdminFlags})
	}
	return out
}
func Command(o *domain.Operation) *pb.Command {
	if o == nil {
		return nil
	}
	return &pb.Command{ConfigurationRevisionId: o.ConfigurationRevisionID, Configuration: ConfigurationDocument(o.Configuration), BaseArtifactId: o.BaseArtifactID, BaseVersion: o.BaseVersion, RollbackArtifactId: o.RollbackArtifactID, ExpectedProcessId: o.ExpectedProcessID, Id: o.ID, Kind: o.Kind, Backend: o.Backend, Server: o.Server, Weight: o.Weight, ExpectedGeneration: o.ExpectedGeneration, ExpectedState: o.ExpectedState, ExpectedWeight: o.ExpectedWeight, DeadlineMs: o.Deadline.UnixMilli()}
}
func ReadCommand(c *pb.Command) domain.Operation {
	if c == nil {
		return domain.Operation{}
	}
	return domain.Operation{ConfigurationRevisionID: c.ConfigurationRevisionId, Configuration: ReadConfigurationDocument(c.Configuration), BaseArtifactID: c.BaseArtifactId, BaseVersion: c.BaseVersion, RollbackArtifactID: c.RollbackArtifactId, ExpectedProcessID: c.ExpectedProcessId, ID: c.Id, Kind: c.Kind, Backend: c.Backend, Server: c.Server, Weight: c.Weight, ExpectedGeneration: c.ExpectedGeneration, ExpectedState: c.ExpectedState, ExpectedWeight: c.ExpectedWeight, Deadline: time.UnixMilli(c.DeadlineMs)}
}
func Result(r *domain.OperationResult) *pb.OperationResult {
	if r == nil {
		return nil
	}
	return &pb.OperationResult{OperationId: r.OperationID, Outcome: r.Outcome, ErrorCode: r.ErrorCode, Discovery: Discovery(r.Discovery), CommandDigest: r.CommandDigest}
}
func ReadResult(r *pb.OperationResult) *domain.OperationResult {
	if r == nil {
		return nil
	}
	return &domain.OperationResult{OperationID: r.OperationId, Outcome: r.Outcome, ErrorCode: r.ErrorCode, Discovery: ReadDiscovery(r.Discovery), CommandDigest: r.CommandDigest}
}

func ConfigurationDocument(d *domain.ConfigurationDocument) *pb.ConfigurationDocument {
	if d == nil {
		return nil
	}
	out := &pb.ConfigurationDocument{}
	for _, l := range d.Listeners {
		out.Listeners = append(out.Listeners, &pb.HTTPSListener{Name: l.Name, Address: l.Address, Port: l.Port, CertificateRef: l.CertificateRef, CertificateSha256: l.CertificateSHA256, NodeCertificates: maps.Clone(l.NodeCertificates)})
	}
	for _, a := range d.Applications {
		v := &pb.HTTPApplication{Id: a.ID, Name: a.Name, Listener: a.Listener, Hostname: a.Hostname, PathPrefix: a.PathPrefix, Algorithm: a.Algorithm, HealthPath: a.HealthPath, Disabled: a.Disabled, RequestTelemetry: a.RequestTelemetry}
		if a.TrafficPolicy != nil {
			v.TrafficPolicy = &pb.TrafficPolicy{Version: a.TrafficPolicy.Version, Mode: a.TrafficPolicy.Mode, UnknownAction: a.TrafficPolicy.UnknownAction}
			for _, r := range a.TrafficPolicy.Rules {
				v.TrafficPolicy.Rules = append(v.TrafficPolicy.Rules, &pb.TrafficRule{Id: r.ID, Name: r.Name, Action: r.Action, PathPrefix: r.PathPrefix, Cidrs: append([]string{}, r.CIDRs...), Countries: append([]string{}, r.Countries...), Asns: append([]uint32{}, r.ASNs...)})
			}
		}
		if a.Verification != nil {
			v.Verification = &pb.ApplicationVerificationPolicy{Http: a.Verification.HTTP, Https: a.Verification.HTTPS, Entrypoint: a.Verification.Entrypoint}
		}
		for _, s := range a.Servers {
			v.Servers = append(v.Servers, &pb.ConfigurationServer{Name: s.Name, Address: s.Address, Port: s.Port, Weight: s.Weight, Maintenance: s.Maintenance, ForwardIncomingPort: s.ForwardIncomingPort})
		}
		out.Applications = append(out.Applications, v)
	}
	for _, b := range d.Backends {
		value := &pb.ConfigurationBackend{Name: b.Name, Algorithm: b.Algorithm}
		for _, s := range b.Servers {
			value.Servers = append(value.Servers, &pb.ConfigurationServer{Name: s.Name, Address: s.Address, Port: s.Port, Weight: s.Weight, Maintenance: s.Maintenance, ForwardIncomingPort: s.ForwardIncomingPort})
		}
		out.Backends = append(out.Backends, value)
	}
	return out
}
func ReadConfigurationDocument(d *pb.ConfigurationDocument) *domain.ConfigurationDocument {
	if d == nil {
		return nil
	}
	out := &domain.ConfigurationDocument{Backends: []domain.ConfigurationBackend{}}
	for _, l := range d.Listeners {
		if l == nil {
			return nil
		}
		out.Listeners = append(out.Listeners, domain.HTTPSListener{ListenerBinding: domain.ListenerBinding{Name: l.Name, Address: l.Address, Port: l.Port}, CertificateRef: l.CertificateRef, CertificateSHA256: l.CertificateSha256, NodeCertificates: maps.Clone(l.NodeCertificates)})
	}
	for _, a := range d.Applications {
		if a == nil {
			return nil
		}
		v := domain.HTTPApplication{ID: a.Id, Name: a.Name, Listener: a.Listener, Hostname: a.Hostname, PathPrefix: a.PathPrefix, Algorithm: a.Algorithm, HealthPath: a.HealthPath, Disabled: a.Disabled, RequestTelemetry: a.RequestTelemetry}
		if a.TrafficPolicy != nil {
			v.TrafficPolicy = &domain.TrafficPolicy{Version: a.TrafficPolicy.Version, Mode: a.TrafficPolicy.Mode, UnknownAction: a.TrafficPolicy.UnknownAction}
			for _, r := range a.TrafficPolicy.Rules {
				if r == nil {
					return nil
				}
				v.TrafficPolicy.Rules = append(v.TrafficPolicy.Rules, domain.TrafficRule{ID: r.Id, Name: r.Name, Action: r.Action, PathPrefix: r.PathPrefix, CIDRs: append([]string(nil), r.Cidrs...), Countries: append([]string(nil), r.Countries...), ASNs: append([]uint32(nil), r.Asns...)})
			}
		}
		if a.Verification != nil {
			v.Verification = &domain.ApplicationVerificationPolicy{HTTP: a.Verification.Http, HTTPS: a.Verification.Https, Entrypoint: a.Verification.Entrypoint}
		}
		for _, s := range a.Servers {
			if s == nil {
				return nil
			}
			v.Servers = append(v.Servers, domain.ConfigurationServer{Name: s.Name, Address: s.Address, Port: s.Port, Weight: s.Weight, Maintenance: s.Maintenance, ForwardIncomingPort: s.ForwardIncomingPort})
		}
		out.Applications = append(out.Applications, v)
	}
	for _, b := range d.Backends {
		if b == nil {
			return nil
		}
		value := domain.ConfigurationBackend{Name: b.Name, Algorithm: b.Algorithm, Servers: []domain.ConfigurationServer{}}
		for _, s := range b.Servers {
			if s == nil {
				return nil
			}
			value.Servers = append(value.Servers, domain.ConfigurationServer{Name: s.Name, Address: s.Address, Port: s.Port, Weight: s.Weight, Maintenance: s.Maintenance, ForwardIncomingPort: s.ForwardIncomingPort})
		}
		out.Backends = append(out.Backends, value)
	}
	return out
}
func ConfigurationState(s *domain.ConfigurationState) *pb.ConfigurationState {
	if s == nil {
		return nil
	}
	out := &pb.ConfigurationState{NodeId: s.NodeID, RequestLogsAllowed: s.RequestLogsAllowed, ApplicationControlVersion: s.ApplicationControlVersion, ListenerControlVersion: s.ListenerControlVersion, Version: s.Version, ArtifactId: s.ArtifactID, Document: ConfigurationDocument(s.Document), ObservedAtMs: s.ObservedAtMS, ControlAllowed: s.ControlAllowed, ErrorCode: s.ErrorCode}
	if s.TrafficRules != nil {
		c := s.TrafficRules
		out.TrafficRules = &pb.TrafficCapabilities{Profile: c.Profile, ClientSource: c.ClientSource, CountryReady: c.CountryReady, AsnReady: c.ASNReady}
	}
	for _, g := range s.ACMEGrants {
		p := &pb.ACMEGrant{Issuer: g.Issuer, Listener: g.Listener, Environment: g.Environment, PolicyDigest: g.PolicyDigest}
		for _, d := range g.Challenges {
			p.Challenges = append(p.Challenges, &pb.ACMEChallenge{Hostname: d.Hostname, Target: d.Target})
		}
		out.AcmeGrants = append(out.AcmeGrants, p)
	}
	for _, b := range s.ListenerBindings {
		out.ListenerBindings = append(out.ListenerBindings, &pb.ListenerBinding{Name: b.Name, Address: b.Address, Port: b.Port})
	}
	for _, c := range s.TLSCertificates {
		out.TlsCertificates = append(out.TlsCertificates, &pb.TLSCertificate{Reference: c.Reference, Sha256: c.SHA256, DnsNames: append([]string{}, c.DNSNames...), NotBeforeMs: c.NotBeforeMS, NotAfterMs: c.NotAfterMS, ErrorCode: c.ErrorCode})
	}
	for _, l := range s.TLSListeners {
		out.TlsListeners = append(out.TlsListeners, &pb.TLSListenerObservation{Name: l.Name, Sha256: l.SHA256, Loaded: l.Loaded})
	}
	return out
}
func ReadConfigurationState(s *pb.ConfigurationState) *domain.ConfigurationState {
	if s == nil {
		return nil
	}
	out := &domain.ConfigurationState{NodeID: s.NodeId, RequestLogsAllowed: s.RequestLogsAllowed, ApplicationControlVersion: s.ApplicationControlVersion, ListenerControlVersion: s.ListenerControlVersion, Version: s.Version, ArtifactID: s.ArtifactId, Document: ReadConfigurationDocument(s.Document), ObservedAtMS: s.ObservedAtMs, ControlAllowed: s.ControlAllowed, ErrorCode: s.ErrorCode}
	if s.TrafficRules != nil {
		c := s.TrafficRules
		out.TrafficRules = &domain.TrafficCapabilities{Profile: c.Profile, ClientSource: c.ClientSource, CountryReady: c.CountryReady, ASNReady: c.AsnReady}
	}
	for _, g := range s.AcmeGrants {
		if g == nil {
			continue
		}
		p := domain.ACMEGrant{Issuer: g.Issuer, Listener: g.Listener, Environment: g.Environment, PolicyDigest: g.PolicyDigest}
		for _, d := range g.Challenges {
			if d != nil {
				p.Challenges = append(p.Challenges, domain.ACMEChallenge{Hostname: d.Hostname, Target: d.Target})
			}
		}
		out.ACMEGrants = append(out.ACMEGrants, p)
	}
	for _, b := range s.ListenerBindings {
		if b == nil {
			return nil
		}
		out.ListenerBindings = append(out.ListenerBindings, domain.ListenerBinding{Name: b.Name, Address: b.Address, Port: b.Port})
	}
	for _, c := range s.TlsCertificates {
		if c == nil {
			return nil
		}
		out.TLSCertificates = append(out.TLSCertificates, domain.TLSCertificate{Reference: c.Reference, SHA256: c.Sha256, DNSNames: append([]string{}, c.DnsNames...), NotBeforeMS: c.NotBeforeMs, NotAfterMS: c.NotAfterMs, ErrorCode: c.ErrorCode})
	}
	for _, l := range s.TlsListeners {
		if l == nil {
			return nil
		}
		out.TLSListeners = append(out.TLSListeners, domain.TLSListenerObservation{Name: l.Name, SHA256: l.Sha256, Loaded: l.Loaded})
	}
	return out
}
