// Package haproxyruntime exposes a fixed set of local Unix-socket operations.
// It never accepts CLI text, reads configuration secrets into reports, or reloads HAProxy.
package haproxyruntime

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ecraela/fleet/internal/domain"
)

type Client struct {
	Socket       string
	ConfigFiles  []string
	AllowControl bool
}

// AdminAccess checks authority without attempting a runtime mutation.
func (c Client) AdminAccess(ctx context.Context) bool {
	level, err := c.command(ctx, "show cli level")
	return err == nil && strings.TrimSpace(level) == "admin"
}

func (c Client) command(ctx context.Context, command string) (string, error) {
	if c.Socket == "" {
		return "", errors.New("runtime_not_configured")
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return "", errors.New("runtime_unavailable")
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	if at, ok := ctx.Deadline(); ok && at.Before(deadline) {
		deadline = at
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err = fmt.Fprintf(conn, "%s\n", command); err != nil {
		return "", errors.New("runtime_unavailable")
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		_ = unix.CloseWrite()
	}
	body, err := io.ReadAll(io.LimitReader(conn, 2<<20+1))
	if err != nil || len(body) > 2<<20 {
		return "", errors.New("runtime_unavailable")
	}
	return string(body), nil
}
func (c Client) fingerprint() (string, error) {
	if len(c.ConfigFiles) == 0 {
		return "", nil
	}
	if len(c.ConfigFiles) > 16 {
		return "", errors.New("too many configuration files")
	}
	paths := append([]string(nil), c.ConfigFiles...)
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return "", err
		}
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() || stat.Size() > 1<<20 {
			file.Close()
			return "", errors.New("invalid configuration file")
		}
		data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
		_ = file.Close()
		if err != nil || len(data) > 1<<20 {
			return "", errors.New("configuration exceeds limit")
		}
		fmt.Fprintf(hash, "%d:", len(data))
		_, _ = hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func integer(raw string) (uint32, error) {
	value, err := strconv.ParseUint(raw, 10, 32)
	return uint32(value), err
}

func (c Client) Discover(ctx context.Context) domain.Discovery {
	d := domain.Discovery{ObservedAtMS: time.Now().UnixMilli(), Proxies: []domain.Proxy{}, Servers: []domain.BackendServer{}}
	failed := func(code string) domain.Discovery {
		version := d.Version
		if len(version) > 128 {
			version = ""
		}
		return domain.Discovery{Version: version, ObservedAtMS: d.ObservedAtMS, ErrorCode: code, Proxies: []domain.Proxy{}, Servers: []domain.BackendServer{}}
	}
	if c.Socket == "" {
		return failed("runtime_not_configured")
	}
	info, err := c.command(ctx, "show info")
	if err != nil {
		return failed("runtime_unavailable")
	}
	for _, line := range strings.Split(info, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "Version":
			d.Version = value
		case "Pid":
			d.ProcessID = value
		case "Start_time_sec":
			d.StartedAt, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	if d.StartedAt <= 0 || !strings.HasPrefix(d.Version, "3.") {
		return failed("unsupported_runtime")
	}
	d.ConfigFingerprint, err = c.fingerprint()
	if err != nil {
		return failed("runtime_unavailable")
	}
	d.ConfigFiles = uint32(len(c.ConfigFiles))
	stats, err := c.command(ctx, "show stat")
	if err != nil {
		return failed("runtime_unavailable")
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimSpace(stats)))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return failed("runtime_unavailable")
	}
	for i := range header {
		header[i] = strings.TrimSpace(strings.TrimPrefix(header[i], "#"))
	}
	serverRows := map[string]map[string]string{}
	for {
		fields, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(fields) != len(header) {
			return failed("runtime_unavailable")
		}
		row := map[string]string{}
		for i, name := range header {
			row[name] = fields[i]
		}
		name := row["svname"]
		if name == "FRONTEND" || name == "BACKEND" {
			kind := "frontend"
			if name == "BACKEND" {
				kind = "backend"
			}
			active, err := integer(row["scur"])
			if err != nil {
				return failed("runtime_unavailable")
			}
			d.Proxies = append(d.Proxies, domain.Proxy{ActiveConnections: &active, Name: row["pxname"], Kind: kind, Mode: row["mode"], Algorithm: row["algo"], Status: row["status"]})
		} else if row["type"] == "2" {
			serverRows[row["pxname"]+"/"+name] = row
		}
		if len(d.Proxies) > domain.MaxProxies || len(serverRows) > domain.MaxBackendServers {
			return failed("runtime_unavailable")
		}
	}
	states, err := c.command(ctx, "show servers state")
	if err != nil {
		return failed("runtime_unavailable")
	}
	var columns []string
	for _, line := range strings.Split(states, "\n") {
		if strings.HasPrefix(line, "#") {
			columns = strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#")))
			continue
		}
		fields := strings.Fields(line)
		if len(fields) <= 1 {
			continue
		}
		if len(columns) == 0 || len(fields) != len(columns) {
			return failed("runtime_unavailable")
		}
		row := map[string]string{}
		for i, name := range columns {
			row[name] = fields[i]
		}
		stat, ok := serverRows[row["be_name"]+"/"+row["srv_name"]]
		if !ok {
			return failed("runtime_unavailable")
		}
		flags, err1 := integer(row["srv_admin_state"])
		weight, err2 := integer(row["srv_uweight"])
		initial, err3 := integer(row["srv_iweight"])
		active, err4 := integer(stat["scur"])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return failed("runtime_unavailable")
		}
		state := "ready"
		if flags&(1|2|4|32|64) != 0 {
			state = "maint"
		} else if flags&(8|16) != 0 {
			state = "drain"
		}
		d.Servers = append(d.Servers, domain.BackendServer{Backend: row["be_name"], Name: row["srv_name"], Address: stat["addr"], State: state, Weight: weight, InitialWeight: initial, ActiveConnections: active, Health: stat["status"], CheckStatus: stat["check_status"], AdminFlags: flags})
	}
	if len(d.Servers) != len(serverRows) {
		return failed("runtime_unavailable")
	}
	d.ControlAllowed = c.AllowControl && strings.HasPrefix(d.Version, "3.4.")
	d.SetGeneration()
	if !d.Valid() {
		return failed("unsupported_runtime")
	}
	return d
}

// Execute uses optimistic preconditions and verifies fresh runtime state. Drain
// completes only after normal selection is disabled and active sessions reach zero.
func (c Client) Execute(ctx context.Context, o domain.Operation, apply bool) (domain.Discovery, string, string) {
	current := c.Discover(ctx)
	if !current.Fresh(time.Now()) || (apply && !current.ControlAllowed) {
		if !apply {
			return current, "unknown", "runtime_unavailable"
		}
		return current, "failed", "runtime_unavailable"
	}
	if current.Generation != o.ExpectedGeneration {
		if !apply {
			return current, "unknown", "generation_changed"
		}
		return current, "failed", "generation_changed"
	}
	target, ok := current.Server(o.Backend, o.Server)
	if !ok {
		if !apply {
			return current, "unknown", "target_missing"
		}
		return current, "failed", "target_missing"
	}
	if apply {
		if target.State != o.ExpectedState || target.Weight != o.ExpectedWeight {
			return current, "failed", "state_changed"
		}
		if (o.Kind == "maintenance" || o.Kind == "drain" || (o.Kind == "weight" && o.Weight == 0)) && !current.HasCapacity(o.Backend, o.Server) {
			return current, "failed", "last_available_server"
		}
		command := ""
		switch o.Kind {
		case "maintenance":
			command = "state maint"
		case "drain":
			command = "state drain"
		case "restore":
			command = "state ready"
		case "weight":
			command = "weight " + strconv.FormatUint(uint64(o.Weight), 10)
		default:
			return current, "failed", "invalid_operation"
		}
		if !domain.ValidRuntimeName(o.Backend) || !domain.ValidRuntimeName(o.Server) || o.Weight > 256 {
			return current, "failed", "invalid_operation"
		}
		output, err := c.command(ctx, "set server "+o.Backend+"/"+o.Server+" "+command)
		if err != nil {
			return current, "unknown", "runtime_unavailable"
		}
		if strings.TrimSpace(output) != "" {
			return current, "failed", "runtime_rejected"
		}
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		current = c.Discover(ctx)
		if current.Fresh(time.Now()) && o.Verified(current) {
			return current, "succeeded", ""
		}
		if current.Generation != "" && current.Generation != o.ExpectedGeneration {
			return current, "unknown", "generation_changed"
		}
		if !apply {
			return current, "unknown", "outcome_unverified"
		}
		select {
		case <-ctx.Done():
			return current, "unknown", "verification_deadline"
		case <-ticker.C:
		}
	}
}
