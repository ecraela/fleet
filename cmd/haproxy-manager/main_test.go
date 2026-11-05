package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDedicatedDatabaseRequired(t *testing.T) {
	t.Setenv("HAPROXY_DATABASE_URL", "")
	t.Setenv("PGDATABASE", "unrelated_converge_database")
	for _, command := range []string{"serve", "migrate"} {
		if err := run([]string{command}); err == nil || !strings.Contains(err.Error(), "HAPROXY_DATABASE_URL") {
			t.Fatalf("%s did not reject absent dedicated database configuration: %v", command, err)
		}
	}
}

func TestInitProtectsExistingInstallation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "installation")
	args := []string{"init", "--state-dir", dir}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "admin-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := run(args); err == nil {
		t.Fatal("init replaced existing credentials")
	}
	after, err := os.ReadFile(filepath.Join(dir, "admin-token"))
	if err != nil || string(before) != string(after) {
		t.Fatal("existing operator credential changed")
	}
	info, err := os.Stat(filepath.Join(dir, "pki", "ca.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("CA private key permissions")
	}
}
