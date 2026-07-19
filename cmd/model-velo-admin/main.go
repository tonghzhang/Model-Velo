package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"model-velo/internal/apikey"
	"model-velo/internal/config"
	"model-velo/internal/postgres"
)

func main() {
	log.SetFlags(0)
	if err := run(context.Background(), os.Args[1:]); err != nil {
		log.Fatalf("model-velo-admin: %v", err)
	}
}

func run(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return usageError()
	}

	postgresSettings, err := config.LoadPostgres()
	if err != nil {
		return fmt.Errorf("configure PostgreSQL: %w", err)
	}
	security, err := config.LoadAPIKeySecurity()
	if err != nil {
		return fmt.Errorf("configure API key security: %w", err)
	}

	database, err := postgres.Open(ctx, postgresSettings)
	if err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	defer database.Close()

	if err := database.SyncSchema(ctx); err != nil {
		return err
	}

	manager, err := apikey.NewManager(database.ORM(), security.Pepper)
	if err != nil {
		return err
	}

	switch arguments[0] {
	case "bootstrap-tenant":
		return bootstrapTenant(ctx, manager, arguments[1:])
	case "create-key":
		return createKey(ctx, manager, arguments[1:])
	case "revoke-key":
		return changeKeyStatus(ctx, manager.Revoke, "revoked", arguments[1:])
	case "disable-key":
		return changeKeyStatus(ctx, manager.Disable, "disabled", arguments[1:])
	default:
		return usageError()
	}
}

func bootstrapTenant(ctx context.Context, manager *apikey.Manager, arguments []string) error {
	flags := flag.NewFlagSet("bootstrap-tenant", flag.ContinueOnError)
	slug := flags.String("slug", "", "lowercase tenant slug")
	name := flags.String("name", "", "tenant display name")
	label := flags.String("label", "bootstrap", "initial API key label")
	models := flags.String("models", "", "comma-separated gateway model names")
	expiresIn := flags.Duration("expires-in", 0, "optional API key lifetime")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError()
	}

	expiresAt, err := expirationFromDuration(*expiresIn)
	if err != nil {
		return err
	}

	issued, err := manager.BootstrapTenant(ctx, apikey.BootstrapTenantInput{
		Slug:        *slug,
		DisplayName: *name,
		KeyLabel:    *label,
		Models:      splitModels(*models),
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return err
	}

	printIssuedKey(issued)
	return nil
}

func createKey(ctx context.Context, manager *apikey.Manager, arguments []string) error {
	flags := flag.NewFlagSet("create-key", flag.ContinueOnError)
	tenantID := flags.String("tenant-id", "", "existing tenant UUID")
	label := flags.String("label", "", "API key label")
	expiresIn := flags.Duration("expires-in", 0, "optional API key lifetime")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError()
	}

	expiresAt, err := expirationFromDuration(*expiresIn)
	if err != nil {
		return err
	}

	issued, err := manager.CreateKey(ctx, apikey.CreateKeyInput{
		TenantID:  *tenantID,
		Label:     *label,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return err
	}

	printIssuedKey(issued)
	return nil
}

func changeKeyStatus(
	ctx context.Context,
	managerAction func(context.Context, string) error,
	status string,
	arguments []string,
) error {
	flags := flag.NewFlagSet(status+"-key", flag.ContinueOnError)
	keyID := flags.String("id", "", "API key UUID")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError()
	}

	if err := managerAction(ctx, *keyID); err != nil {
		return err
	}
	fmt.Printf("api_key_id=%s\nstatus=%s\n", *keyID, status)
	return nil
}

func expirationFromDuration(duration time.Duration) (*time.Time, error) {
	if duration < 0 {
		return nil, errors.New("expires-in must not be negative")
	}
	if duration == 0 {
		return nil, nil
	}
	expiresAt := time.Now().UTC().Add(duration)
	return &expiresAt, nil
}

func splitModels(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func printIssuedKey(issued apikey.IssuedKey) {
	fmt.Printf("tenant_id=%s\n", issued.TenantID)
	fmt.Printf("api_key_id=%s\n", issued.ID)
	fmt.Printf("api_key_prefix=%s\n", issued.Prefix)
	if issued.ExpiresAt != nil {
		fmt.Printf("expires_at=%s\n", issued.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Println("api_key=" + issued.Plaintext)
	fmt.Println("warning=store this key now; Model-Velo cannot recover it later")
}

func usageError() error {
	return errors.New(
		"usage: model-velo-admin <bootstrap-tenant|create-key|revoke-key|disable-key> [flags]",
	)
}
