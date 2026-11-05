package accesslog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrStorage = errors.New("request telemetry storage unavailable")

type Storage struct {
	base   *url.URL
	client *http.Client
}

// The URL is installation configuration, never a browser-supplied destination.
func NewStorage(base string, client *http.Client) (*Storage, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("logs URL must be an HTTP(S) service origin")
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{MaxConnsPerHost: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 2 * time.Second}}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Storage{base: u, client: &c}, nil
}
func (s *Storage) endpoint(path string) string { u := *s.base; u.Path = path; return u.String() }
func (s *Storage) Insert(ctx context.Context, rows []map[string]any) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return ErrStorage
		}
	}
	u, _ := url.Parse(s.endpoint("/insert/jsonline"))
	u.RawQuery = url.Values{"_stream_fields": {"organization_id", "cluster_id", "node_id", "kind", "application_id"}, "_time_field": {"at_ms"}, "_msg_field": {"message"}}.Encode()
	// VictoriaLogs expects a comma-separated stream field argument.
	q := u.Query()
	q.Set("_stream_fields", "organization_id,cluster_id,node_id,kind,application_id")
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, "POST", u.String(), &body)
	req.Header.Set("Content-Type", "application/stream+json")
	resp, err := s.client.Do(req)
	if err != nil {
		return ErrStorage
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrStorage
	}
	return nil
}

// Query has a fixed transport path and bounded response; callers construct an
// allowlisted query from validated IDs and numeric filters, never raw LogsQL.
func (s *Storage) Query(ctx context.Context, query string, startMS, endMS int64, maxRows int) ([]map[string]string, error) {
	values := url.Values{"query": {query}, "start": {strconv.FormatFloat(float64(startMS)/1000, 'f', 3, 64)}, "end": {strconv.FormatFloat(float64(endMS)/1000, 'f', 3, 64)}, "timeout": {"2s"}}
	req, _ := http.NewRequestWithContext(ctx, "POST", s.endpoint("/select/logsql/query"), strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, ErrStorage
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, ErrStorage
	}
	limited := io.LimitReader(resp.Body, 2<<20+1)
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	rows := []map[string]string{}
	size := 0
	for scanner.Scan() {
		size += len(scanner.Bytes()) + 1
		if size > 2<<20 || len(rows) >= maxRows {
			return nil, ErrStorage
		}
		row := map[string]string{}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			return nil, ErrStorage
		}
		rows = append(rows, row)
	}
	if scanner.Err() != nil {
		return nil, ErrStorage
	}
	return rows, nil
}
