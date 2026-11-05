package accesslog

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type Receiver struct {
	conn                                              *net.UnixConn
	path                                              string
	file                                              os.FileInfo
	boot                                              string
	queue                                             chan Event
	policy                                            *PathPolicy
	geo                                               *GeoResolver
	pending                                           *Event
	received, ignored, rejected, dropped, unconfirmed atomic.Uint64
	listening                                         atomic.Bool
}

func Open(path string) (*Receiver, error) {
	return OpenWithPathPolicy(path, nil)
}

func OpenWithPathPolicy(path string, policy *PathPolicy) (*Receiver, error) {
	return OpenWithPolicies(path, policy, nil)
}

func OpenWithPolicies(path string, policy *PathPolicy, geo *GeoResolver) (*Receiver, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 100 {
		return nil, errors.New("access-log socket must be an absolute local path of at most 100 bytes")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("access-log socket directory unavailable")
	}
	if f, err := os.Lstat(path); err == nil {
		if f.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("access-log destination is not a socket")
		}
		probe, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
		if err == nil {
			probe.Close()
			return nil, errors.New("access-log socket is already in use")
		}
		if err = os.Remove(path); err != nil {
			return nil, errors.New("stale access-log socket cannot be removed")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("access-log socket unavailable")
	}
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, errors.New("access-log socket cannot listen")
	}
	if err = os.Chmod(path, 0660); err != nil {
		c.Close()
		os.Remove(path)
		return nil, err
	}
	f, err := os.Lstat(path)
	if err != nil {
		c.Close()
		return nil, err
	}
	return &Receiver{conn: c, path: path, file: f, boot: domain.NewID(), queue: make(chan Event, QueueSize), policy: policy, geo: geo}, nil
}
func (r *Receiver) Close() {
	r.conn.Close()
	if f, err := os.Lstat(r.path); err == nil && os.SameFile(f, r.file) {
		_ = os.Remove(r.path)
	}
}
func (r *Receiver) Run(ctx context.Context) {
	r.listening.Store(true)
	defer r.listening.Store(false)
	var sequence uint64
	raw := make([]byte, 2049)
	for ctx.Err() == nil {
		_ = r.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := r.conn.ReadFromUnix(raw)
		if err != nil {
			if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
				continue
			}
			return
		}
		sequence++
		e, err := parseGeo(raw[:n], r.boot, sequence, time.Now(), r.policy, r.geo)
		if errors.Is(err, ErrIgnored) {
			r.ignored.Add(1)
			continue
		}
		if err != nil {
			r.rejected.Add(1)
			continue
		}
		r.received.Add(1)
		select {
		case r.queue <- e:
		default:
			r.dropped.Add(1)
		}
	}
}
func (r *Receiver) Drain() []Event {
	events := make([]Event, 0, MaxBatch)
	bytes := 0
	for len(events) < MaxBatch {
		var e Event
		if r.pending != nil {
			e, r.pending = *r.pending, nil
		} else {
			select {
			case e = <-r.queue:
			default:
				return events
			}
		}
		encoded, _ := json.Marshal(e)
		if bytes+len(encoded)+1 > MaxPayload-4096 {
			r.pending = &e
			break
		}
		bytes += len(encoded) + 1
		events = append(events, e)
	}
	return events
}
func (r *Receiver) Snapshot() Pipeline {
	p := Pipeline{Listening: r.listening.Load(), BootID: r.boot, AtMS: time.Now().UnixMilli(), Received: r.received.Load(), Ignored: r.ignored.Load(), Rejected: r.rejected.Load(), Dropped: r.dropped.Load(), Unconfirmed: r.unconfirmed.Load(), PathMode: "off"}
	if r.policy != nil {
		p.PathMode, p.PathPolicy = r.policy.Mode, r.policy.digest
	}
	p.Geo = r.geo.Snapshot()
	return p
}
func (r *Receiver) Unconfirmed(count int) { r.unconfirmed.Add(uint64(count)) }
