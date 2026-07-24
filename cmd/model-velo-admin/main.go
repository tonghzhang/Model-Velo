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

	"model-velo/internal/adminauth"
	"model-velo/internal/apikey"
	"model-velo/internal/config"
	"model-velo/internal/postgres"
	"model-velo/internal/usage"
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
	database, err := postgres.Open(ctx, postgresSettings)
	if err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	defer database.Close()

	if err := database.SyncSchema(ctx); err != nil {
		return err
	}
	if arguments[0] == "reprice-usage" {
		return repriceUsage(ctx, database, arguments[1:])
	}
	if arguments[0] == "bootstrap-admin" {
		return bootstrapAdmin(ctx, database, arguments[1:])
	}

	security, err := config.LoadAPIKeySecurity()
	if err != nil {
		return fmt.Errorf("configure API key security: %w", err)
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

func bootstrapAdmin(
	ctx context.Context,
	database *postgres.Database,
	arguments []string,
) error {
	flags := flag.NewFlagSet("bootstrap-admin", flag.ContinueOnError)
	name := flags.String("name", "root", "unique admin principal name")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError()
	}
	pepper, err := config.LoadAdminKeySecurity()
	if err != nil {
		return err
	}
	manager, err := adminauth.NewManager(database.ORM(), pepper)
	if err != nil {
		return err
	}
	issued, err := manager.Bootstrap(ctx, *name)
	if err != nil {
		return err
	}
	fmt.Printf("admin_principal_id=%s\n", issued.PrincipalID)
	fmt.Printf("admin_key_prefix=%s\n", issued.KeyPrefix)
	fmt.Println("admin_key=" + issued.Plaintext)
	fmt.Println("warning=store this key now; Model-Velo cannot recover it later")
	return nil
}

func repriceUsage(ctx context.Context, database *postgres.Database, arguments []string) error {
	flags := flag.NewFlagSet("reprice-usage", flag.ContinueOnError)
	startText := flags.String("start", time.Now().UTC().Add(-30*24*time.Hour).Format(time.RFC3339), "inclusive RFC3339 start")
	endText := flags.String("end", time.Now().UTC().Format(time.RFC3339), "exclusive RFC3339 end")
	tenantID := flags.String("tenant-id", "", "optional tenant UUID")
	providerID := flags.String("provider", "", "optional provider ID")
	model := flags.String("model", "", "optional gateway model")
	missingOnly := flags.Bool("missing-only", true, "only fill records without known cost")
	limit := flags.Int("limit", 1000, "maximum records to process")
	cursor := flags.String("cursor", "", "opaque cursor returned by a previous batch")
	confirm := flags.Bool("confirm", false, "confirm stored cost updates")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError()
	}
	if !*confirm {
		return errors.New("reprice-usage requires --confirm")
	}
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(*startText))
	if err != nil {
		return errors.New("reprice-usage start must be RFC3339")
	}
	end, err := time.Parse(time.RFC3339, strings.TrimSpace(*endText))
	if err != nil {
		return errors.New("reprice-usage end must be RFC3339")
	}
	settings, err := config.LoadUsage()
	if err != nil {
		return fmt.Errorf("configure usage: %w", err)
	}
	pricing, err := usage.NewPricingCatalog(settings.Pricing)
	if err != nil {
		return fmt.Errorf("configure usage pricing: %w", err)
	}
	store, err := usage.NewStore(database.ORM(), pricing)
	if err != nil {
		return err
	}
	result, err := store.Reprice(ctx, usage.RepriceParams{
		TenantID:    *tenantID,
		Start:       start,
		End:         end,
		ProviderID:  *providerID,
		Model:       *model,
		MissingOnly: *missingOnly,
		Limit:       *limit,
		Cursor:      *cursor,
	})
	if err != nil {
		return err
	}
	fmt.Printf(
		"matched=%d\npriced=%d\nunknown=%d\nnext_cursor=%s\n",
		result.Matched,
		result.Priced,
		result.Unknown,
		result.NextCursor,
	)
	return nil
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
		"usage: model-velo-admin <bootstrap-admin|bootstrap-tenant|create-key|revoke-key|disable-key|reprice-usage> [flags]",
	)
}
