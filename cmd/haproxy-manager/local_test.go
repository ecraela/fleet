package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalPasswordFileProtectionAndSpaces(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "password")
	password := "  a synthetic passphrase with spaces  "
	if err := os.WriteFile(file, []byte(password+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if actual, err := readLocalPassword(file); err != nil || actual != password {
		t.Fatal("password spaces were trimmed")
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalPassword(file); err == nil {
		t.Fatal("public password file accepted")
	}
	link := filepath.Join(dir, "linked")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalPassword(link); err == nil {
		t.Fatal("symlink password accepted")
	}
	if _, err := readLocalPassword(dir); err == nil {
		t.Fatal("directory password accepted")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Repeat("x", 1026)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalPassword(file); err == nil {
		t.Fatal("oversize password accepted")
	}
}

func TestCommandHelpDoesNotExposeDatabaseCredential(t *testing.T) {
	t.Setenv("HAPROXY_DATABASE_URL", "postgres://private-user:synthetic-do-not-print@localhost/database")
	for _, args := range [][]string{{"user", "add", "-h"}, {"serve", "-h"}, {"migrate", "-h"}} {
		file, err := os.CreateTemp(t.TempDir(), "help")
		if err != nil {
			t.Fatal(err)
		}
		previous := os.Stderr
		os.Stderr = file
		err = run(args)
		os.Stderr = previous
		file.Close()
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatal("help did not stop before connecting")
		}
		body, e := os.ReadFile(file.Name())
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(body), "synthetic-do-not-print") || strings.Contains(string(body), "private-user") {
			t.Fatal("help exposed the database credential")
		}
	}
}
