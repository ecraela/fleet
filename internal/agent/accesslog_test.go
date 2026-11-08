package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/accesslog"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/pki"
)

type burstAccessSource struct {
	remaining   int
	boot        string
	unconfirmed int
}

func (s *burstAccessSource) Drain() []accesslog.Event {
	count := min(accesslog.MaxBatch, s.remaining)
	s.remaining -= count
	return make([]accesslog.Event, count)
}
func (s *burstAccessSource) Snapshot() accesslog.Pipeline {
	return accesslog.Pipeline{Listening: true, BootID: s.boot, AtMS: time.Now().UnixMilli()}
}
func (s *burstAccessSource) Unconfirmed(count int) { s.unconfirmed += count }

func TestAccessSenderDrainsBurstThroughMTLS(t *testing.T) {
	const burstRecords = 2 * accesslog.QueueSize
	base := t.TempDir()
	pkiDir := filepath.Join(base, "pki")
	if err := pki.Init(pkiDir, nil, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	authority, err := pki.Load(pkiDir, true)
	if err != nil {
		t.Fatal(err)
	}
	serverPair, err := tls.LoadX509KeyPair(filepath.Join(pkiDir, "server.crt"), filepath.Join(pkiDir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	node := domain.Node{ID: domain.NewID(), OrganizationID: domain.NewID(), ClusterID: domain.NewID(), Name: "haproxy-1"}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := csrPEM(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := authority.Issue(csr, node, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(base, "state")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveState(filepath.Join(stateDir, "identity.json"), &identity{Node: node, CertificatePEM: string(cert), CAPEM: string(authority.PEM), KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}); err != nil {
		t.Fatal(err)
	}
	var received atomic.Int32
	complete := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs/write" || len(r.TLS.PeerCertificates) != 1 {
			t.Error("request missed the authenticated log endpoint")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var batch accesslog.Batch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch.Events) != accesslog.MaxBatch {
			t.Error("sender did not upload a full batch", err, len(batch.Events))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if received.Add(int32(len(batch.Events))) == burstRecords {
			complete <- struct{}{}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = authority.TLSConfig(true)
	server.TLS.Certificates = []tls.Certificate{serverPair}
	server.StartTLS()
	defer server.Close()
	receiver := &burstAccessSource{remaining: burstRecords, boot: domain.NewID()}
	trust := x509.NewCertPool()
	trust.AppendCertsFromPEM(authority.PEM)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() {
		sendAccessLoop(ctx, Config{IngestURL: server.URL, StateDir: stateDir}, trust, receiver)
		close(done)
	}()
	select {
	case <-complete:
		// The handler signals before the final 204 reaches the client. Let
		// that in-flight request finish before cancelling the sender.
		time.Sleep(100 * time.Millisecond)
	case <-time.After(1600 * time.Millisecond):
		t.Errorf("sender uploaded %d of %d records within 1.6 s", received.Load(), burstRecords)
	}
	cancel()
	<-done
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("sender or shutdown took %s", elapsed)
	}
	if receiver.remaining != 0 || receiver.unconfirmed != 0 {
		t.Errorf("unexpected collection loss: remaining=%d unconfirmed=%d", receiver.remaining, receiver.unconfirmed)
	}
}
