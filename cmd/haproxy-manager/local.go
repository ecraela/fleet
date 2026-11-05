package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/ecraela/fleet/internal/domain"
	"github.com/ecraela/fleet/internal/localauth"
	"github.com/ecraela/fleet/internal/store"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

// Host/database authority bootstraps and recovers local accounts. There is no
// public signup route, default password, or enrollment-token login shortcut.
func localUser(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: haproxy-manager user add|reset-password|disable [options]")
	}
	action := args[0]
	flags := flag.NewFlagSet("user "+action, flag.ContinueOnError)
	dsn := flags.String("database-url", "", "dedicated PostgreSQL URL (or HAPROXY_DATABASE_URL)")
	username := flags.String("username", "", "local username")
	org := flags.String("organization-id", os.Getenv("HAPROXY_ORGANIZATION_ID"), "tenant identity for new account")
	role := flags.String("role", "viewer", "administrator or viewer for new account")
	passwordFile := flags.String("password-file", "", "private regular file containing the password (never a command-line password)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *dsn == "" {
		*dsn = os.Getenv("HAPROXY_DATABASE_URL")
	}
	name, err := localauth.Username(*username)
	if err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*dsn) == "" {
		return errors.New("dedicated database URL is required")
	}
	if action != "add" && action != "reset-password" && action != "disable" {
		return errors.New("unknown local user command")
	}
	hash := ""
	if action != "disable" {
		password, err := readLocalPassword(*passwordFile)
		if err != nil {
			return err
		}
		hash, err = localauth.Hash(password)
		if err != nil {
			return err
		}
	}
	if action == "add" && (!domain.ValidID(*org) || (*role != "administrator" && *role != "viewer")) {
		return errors.New("new account requires organization-id and administrator or viewer role")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.NewPostgres(ctx, *dsn)
	if err != nil {
		return errors.New("cannot connect to local account database")
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		return errors.New("cannot migrate local account database")
	}
	if action == "add" {
		err = db.CreateLocalUser(ctx, domain.LocalUser{ID: domain.NewID(), Username: name, OrganizationID: *org, PasswordHash: hash, Role: *role})
	} else {
		u, e := db.GetLocalUser(ctx, name)
		if e != nil {
			return errors.New("local account not found")
		}
		if action == "disable" {
			err = db.DisableLocalUser(ctx, u.ID)
		} else {
			err = db.ChangeLocalPassword(ctx, u.ID, u.PasswordHash, hash)
		}
	}
	if err != nil {
		return errors.New("local account change failed; check account name and installation configuration")
	}
	fmt.Println("Local account updated. Existing sessions are revoked on password reset or disable.")
	return nil
}
func readLocalPassword(path string) (string, error) {
	if path == "" {
		return "", errors.New("--password-file is required")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("cannot open password file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1025 {
		return "", errors.New("password file must be private, regular and at most 1025 bytes")
	}
	body, err := io.ReadAll(io.LimitReader(f, 1026))
	if err != nil || len(body) > 1025 {
		return "", errors.New("cannot read password file")
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(body), "\n"), "\r"), nil
}
