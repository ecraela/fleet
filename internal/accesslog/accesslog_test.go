package accesslog

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

func sampleLine() string {
	return "cv1 cv_app_00000000000000000000000000000001 " + strings.Repeat("a", 64) + " web GET 200 4096 23 12 4 0 1 --"
}
func TestPrivateFormatRequiresCompilerIdentityAndBoundedNumbers(t *testing.T) {
	now := time.Now()
	boot := domain.NewID()
	e, err := Parse([]byte(sampleLine()), boot, 1, now)
	if err != nil || e.ApplicationID != "00000000-0000-0000-0000-000000000001" || e.Scheme != "https" || e.DurationMS != 23 || e.AtMS != now.UnixMilli() {
		t.Fatal(e, err)
	}
	for _, line := range []string{strings.Replace(sampleLine(), " 23 ", " NaN ", 1), strings.Replace(sampleLine(), " 200 ", " 999 ", 1), strings.Replace(sampleLine(), " 4096 ", " -1 ", 1), sampleLine() + " Authorization:secret", strings.Repeat("x", 2049)} {
		if _, err := Parse([]byte(line), boot, 1, now); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsafe format accepted", err)
		}
	}
	for _, line := range []string{"GET /?token=private", strings.Replace(sampleLine(), "cv_app_00000000000000000000000000000001", "unmanaged", 1), strings.Replace(sampleLine(), strings.Repeat("a", 64), "-", 1)} {
		if _, err := Parse([]byte(line), boot, 1, now); !errors.Is(err, ErrIgnored) {
			t.Fatal("unmanaged request attributed", err)
		}
	}
	e, err = Parse([]byte(strings.Replace(sampleLine(), " GET ", " GET|HEAD ", 1)), boot, 2, now)
	if err != nil || e.Method != "OTHER" {
		t.Fatal("untrusted method was not classified", e, err)
	}
	for _, change := range []func(*Event){func(e *Event) { e.AtMS = now.Add(-61 * time.Second).UnixMilli() }, func(e *Event) { e.AtMS = now.Add(2 * time.Second).UnixMilli() }, func(e *Event) { e.SpecDigest = "header-supplied" }, func(e *Event) { e.ID = "bad" }, func(e *Event) { e.Termination = "\n\r" }} {
		bad := e
		change(&bad)
		if bad.Valid(now) {
			t.Fatal("invalid wire event accepted")
		}
	}
}
func TestLocalReceiverBoundsQueueAndDoesNotReplaceFilesOrActiveSockets(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "cv-logs-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "access.sock")
	if err = os.WriteFile(path, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := Open(path); err == nil {
		r.Close()
		t.Fatal("replaced regular file")
	}
	os.Remove(path)
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if f, _ := os.Lstat(path); f.Mode().Perm() != 0660 {
		t.Fatal("socket permissions", f.Mode())
	}
	if other, err := Open(path); err == nil {
		other.Close()
		t.Fatal("replaced an active receiver")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < QueueSize+64; i++ {
		if i%16 == 0 {
			time.Sleep(time.Millisecond)
		}
		if _, err = c.Write([]byte(sampleLine())); err != nil {
			if errors.Is(err, syscall.ENOBUFS) {
				i--
				time.Sleep(time.Millisecond)
				continue
			}
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.Snapshot().Dropped == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.Snapshot().Dropped == 0 || len(r.queue) != QueueSize || len(r.Drain()) != MaxBatch {
		t.Fatal("queue was not bounded", r.Snapshot())
	}
	r.Unconfirmed(10)
	if r.Snapshot().Unconfirmed != 10 {
		t.Fatal("delivery ambiguity hidden")
	}
	cancel()
	r.Close()
	<-done
	if r.Snapshot().Listening {
		t.Fatal("closed socket reported listening")
	}
}
func TestRequestBudgetBoundsNodeChurnAndRecovers(t *testing.T) {
	b := NewBudget()
	now := time.Now()
	for i := 0; i < 100; i++ {
		if !b.Admit("one", now) {
			t.Fatal("early limit")
		}
	}
	if b.Admit("one", now) {
		t.Fatal("node rate unbounded")
	}
	for i := 0; i < 1023; i++ {
		if !b.Admit(fmt.Sprint(i), now) {
			t.Fatal("early installation limit")
		}
	}
	if b.Admit("extra", now) {
		t.Fatal("unbounded identity churn")
	}
	if !b.Admit("extra", now.Add(11*time.Second)) || len(b.nodes) != 1 {
		t.Fatal("expired reservations not reclaimed")
	}
}
