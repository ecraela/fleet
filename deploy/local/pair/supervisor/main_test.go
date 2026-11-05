package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestShutdownFinishesProxyWorkAndStopsTheAPI(t *testing.T) {
	directory := t.TempDir()
	child := func(role string) (*exec.Cmd, <-chan error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShutdownHelper$")
		command.Env = append(os.Environ(), "HAPROXY_SHUTDOWN_HELPER="+role, "HAPROXY_SHUTDOWN_DIRECTORY="+directory)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(directory, role+".ready")); err == nil {
				return command, done
			}
			if time.Now().After(deadline) {
				t.Fatal("shutdown helper did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	proxy, proxyDone := child("proxy")
	api, apiDone := child("api")
	shutdown(proxy, proxyDone, api, apiDone)
	for role, process := range map[string]*exec.Cmd{"proxy": proxy, "api": api} {
		if !process.ProcessState.Success() {
			t.Fatalf("%s received the wrong shutdown signal", role)
		}
		if _, err := os.Stat(filepath.Join(directory, role+".finished")); err != nil {
			t.Fatalf("%s work was interrupted: %v", role, err)
		}
	}
}

func TestShutdownHelper(t *testing.T) {
	role := os.Getenv("HAPROXY_SHUTDOWN_HELPER")
	if role == "" {
		return
	}
	directory := os.Getenv("HAPROXY_SHUTDOWN_DIRECTORY")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGUSR1)
	if err := os.WriteFile(filepath.Join(directory, role+".ready"), []byte("ready"), 0600); err != nil {
		os.Exit(2)
	}
	expected := syscall.SIGTERM
	if role == "proxy" {
		expected = syscall.SIGUSR1
	}
	if <-signals != expected {
		os.Exit(3)
	}
	time.Sleep(25 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(directory, role+".finished"), []byte("finished"), 0600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
