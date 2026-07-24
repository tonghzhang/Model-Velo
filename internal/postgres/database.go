package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"model-velo/internal/config"
)

var ErrUnavailable = errors.New("PostgreSQL is unavailable")

type Database struct {
	orm       *gorm.DB
	sql       *sql.DB
	closeOnce sync.Once
	closeErr  error
}

func Open(ctx context.Context, settings config.Postgres) (*Database, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}

	orm, err := gorm.Open(
		gormpostgres.Open(settings.DSN),
		&gorm.Config{
			DisableAutomaticPing: true,
			TranslateError:       true,
			Logger: gormlogger.New(databaseLogWriter{}, gormlogger.Config{
				SlowThreshold:             time.Second,
				LogLevel:                  gormlogger.Warn,
				IgnoreRecordNotFoundError: true,
				ParameterizedQueries:      true,
				Colorful:                  false,
			}),
		},
	)
	if err != nil {
		return nil, errors.New("open PostgreSQL with GORM")
	}

	sqlDatabase, err := orm.DB()
	if err != nil {
		return nil, errors.New("access PostgreSQL connection pool")
	}

	configurePool(sqlDatabase, settings)

	pingContext, cancel := context.WithTimeout(ctx, settings.ConnectTimeout)
	defer cancel()

	if err := sqlDatabase.PingContext(pingContext); err != nil {
		_ = sqlDatabase.Close()
		return nil, safeConnectionError(err)
	}

	return &Database{
		orm: orm,
		sql: sqlDatabase,
	}, nil
}

type databaseLogWriter struct{}

func (databaseLogWriter) Printf(format string, values ...any) {
	detail := strings.TrimSpace(fmt.Sprintf(format, values...))
	if detail != "" {
		slog.Warn("database operation", "detail", detail)
	}
}

func configurePool(database *sql.DB, settings config.Postgres) {
	database.SetMaxOpenConns(settings.MaxOpenConns)
	database.SetMaxIdleConns(settings.MaxIdleConns)
	database.SetConnMaxLifetime(settings.MaxConnLifetime)
	database.SetConnMaxIdleTime(settings.MaxConnIdleTime)
}

func (database *Database) SyncSchema(ctx context.Context) error {
	if err := database.orm.WithContext(ctx).AutoMigrate(
		&Tenant{},
		&APIKey{},
		&TenantModelGrant{},
		&UsageEvent{},
		&UsageOutbox{},
		&AdminPrincipal{},
		&AdminRoleGrant{},
		&RuntimeConfigVersion{},
		&ManagedPricing{},
		&AuditLog{},
		&TenantQuotaPolicy{},
		&QuotaWindow{},
		&QuotaReservation{},
	); err != nil {
		return fmt.Errorf("sync PostgreSQL schema: %w", err)
	}

	return nil
}

func (database *Database) ORM() *gorm.DB {
	return database.orm
}

func (database *Database) SQL() *sql.DB {
	if database == nil {
		return nil
	}
	return database.sql
}

func (database *Database) Close() error {
	database.closeOnce.Do(func() {
		database.closeErr = database.sql.Close()
	})
	return database.closeErr
}

func safeConnectionError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("ping PostgreSQL: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("ping PostgreSQL: %w", context.DeadlineExceeded)
	default:
		return fmt.Errorf("ping PostgreSQL: %w", ErrUnavailable)
	}
}
