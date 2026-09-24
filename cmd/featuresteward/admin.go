package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Melmonster13/featuresteward/internal/audit"
	"github.com/Melmonster13/featuresteward/internal/auth"
	"github.com/Melmonster13/featuresteward/internal/store"
)

// createAdmin creates an admin user and prints a new API token for them.
// It needs direct database access, so it is the only way to get the
// first admin; the API can't be used without a token.
func createAdmin(args []string) error {
	fs := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	handle := fs.String("handle", "", "admin's handle, e.g. mel (required)")
	name := fs.String("name", "", "admin's display name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *handle == "" {
		fs.Usage()
		return errors.New("--handle is required")
	}

	ctx := context.Background()
	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	s := store.NewPostgres(pool)

	if _, err := s.CreateUser(ctx, audit.SystemActor, *handle, *name, auth.RoleAdmin); err != nil {
		return fmt.Errorf("create user %q: %w", *handle, err)
	}
	secret, hash, prefix := auth.NewSecret(auth.TokenPrefix)
	if _, err := s.CreateToken(ctx, audit.SystemActor, *handle, "create-admin", hash, prefix, nil); err != nil {
		return fmt.Errorf("create token: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Created admin %q. Their API token is below; it won't be shown again.\n", *handle)
	fmt.Println(secret)
	return nil
}
