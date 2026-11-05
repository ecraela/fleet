package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestDedicatedBrowserSecretSources(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	if secret, err := loadBrowserSecret("", encoded); err != nil || len(secret) != 32 {
		t.Fatal("dedicated environment secret was not decoded")
	}
	if secret, err := loadBrowserSecret("", ""); err != nil || secret != nil {
		t.Fatal("disabled browser secret fell back to another credential")
	}
	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte(encoded+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if secret, err := loadBrowserSecret(file, ""); err != nil || len(secret) != 32 {
		t.Fatal("private dedicated secret file was not decoded")
	}
	if _, err := loadBrowserSecret(file, encoded); err == nil {
		t.Fatal("ambiguous secret sources accepted")
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBrowserSecret(file, ""); err == nil {
		t.Fatal("public signing secret accepted")
	}
	link := file + "-link"
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBrowserSecret(link, ""); err == nil {
		t.Fatal("signing-secret symlink accepted")
	}
	for _, bad := range []string{"not base64", base64.StdEncoding.EncodeToString([]byte("too short"))} {
		if _, err := loadBrowserSecret("", bad); err == nil {
			t.Fatal("invalid signing secret accepted")
		}
	}
}
