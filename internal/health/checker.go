package health

import (
	"context"
	"database/sql"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type Checker struct {
	database *sql.DB
	redis    *goredis.Client
	timeout  time.Duration
}

func NewChecker(database *sql.DB, redis *goredis.Client, timeout time.Duration) *Checker {
	return &Checker{database: database, redis: redis, timeout: timeout}
}

func (checker *Checker) Check(ctx context.Context) map[string]error {
	result := map[string]error{}
	checkContext, cancel := context.WithTimeout(ctx, checker.timeout)
	defer cancel()
	if checker.database == nil {
		result["postgres"] = sql.ErrConnDone
	} else {
		result["postgres"] = checker.database.PingContext(checkContext)
	}
	if checker.redis == nil {
		result["redis"] = goredis.ErrClosed
	} else {
		result["redis"] = checker.redis.Ping(checkContext).Err()
	}
	return result
}
