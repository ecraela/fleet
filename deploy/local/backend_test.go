package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStreamWaitsForFixtureRelease(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "release")
	t.Setenv("DEMO_STREAM_GATE_FILE", gate)
	server := httptest.NewServer(demoHandler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/?stream_gate=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, reader); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("stream finished before the fixture released it: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("released stream did not finish normally: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("fixture release did not finish the stream")
	}
}

func TestStreamGateRequiresLocalOptIn(t *testing.T) {
	t.Setenv("DEMO_STREAM_GATE_FILE", "")
	server := httptest.NewServer(demoHandler())
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Second
	response, err := client.Get(server.URL + "/?stream_gate=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
}
