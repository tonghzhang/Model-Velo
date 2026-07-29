package observability

import (
	"database/sql"

	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"
)

type dependencyCollector struct {
	database *sql.DB
	redis    *goredis.Client

	postgresConnections *prometheus.Desc
	postgresWaits       *prometheus.Desc
	postgresWaitTime    *prometheus.Desc
	redisConnections    *prometheus.Desc
	redisPoolEvents     *prometheus.Desc
	redisWaitTime       *prometheus.Desc
}

func (metrics *Metrics) RegisterDependencies(
	database *sql.DB,
	redis *goredis.Client,
) error {
	if metrics == nil || (database == nil && redis == nil) {
		return nil
	}
	return metrics.registry.Register(newDependencyCollector(database, redis))
}

func newDependencyCollector(
	database *sql.DB,
	redis *goredis.Client,
) *dependencyCollector {
	return &dependencyCollector{
		database: database,
		redis:    redis,
		postgresConnections: prometheus.NewDesc(
			"model_velo_postgres_connections",
			"Current PostgreSQL connection-pool state.",
			[]string{"state"}, nil,
		),
		postgresWaits: prometheus.NewDesc(
			"model_velo_postgres_waits_total",
			"PostgreSQL connection-pool waits.",
			nil, nil,
		),
		postgresWaitTime: prometheus.NewDesc(
			"model_velo_postgres_wait_duration_seconds_total",
			"Total time waiting for a PostgreSQL connection.",
			nil, nil,
		),
		redisConnections: prometheus.NewDesc(
			"model_velo_redis_pool_connections",
			"Current Redis connection-pool state.",
			[]string{"state"}, nil,
		),
		redisPoolEvents: prometheus.NewDesc(
			"model_velo_redis_pool_events_total",
			"Redis connection-pool cumulative outcomes.",
			[]string{"event"}, nil,
		),
		redisWaitTime: prometheus.NewDesc(
			"model_velo_redis_pool_wait_duration_seconds_total",
			"Total time waiting for a Redis connection.",
			nil, nil,
		),
	}
}

func (collector *dependencyCollector) Describe(output chan<- *prometheus.Desc) {
	output <- collector.postgresConnections
	output <- collector.postgresWaits
	output <- collector.postgresWaitTime
	output <- collector.redisConnections
	output <- collector.redisPoolEvents
	output <- collector.redisWaitTime
}

func (collector *dependencyCollector) Collect(output chan<- prometheus.Metric) {
	if collector.database != nil {
		stats := collector.database.Stats()
		for state, value := range map[string]int{
			"open":     stats.OpenConnections,
			"in_use":   stats.InUse,
			"idle":     stats.Idle,
			"max_open": stats.MaxOpenConnections,
		} {
			output <- prometheus.MustNewConstMetric(
				collector.postgresConnections,
				prometheus.GaugeValue,
				float64(value),
				state,
			)
		}
		output <- prometheus.MustNewConstMetric(
			collector.postgresWaits,
			prometheus.CounterValue,
			float64(stats.WaitCount),
		)
		output <- prometheus.MustNewConstMetric(
			collector.postgresWaitTime,
			prometheus.CounterValue,
			stats.WaitDuration.Seconds(),
		)
	}

	if collector.redis == nil {
		return
	}
	stats := collector.redis.PoolStats()
	for state, value := range map[string]uint32{
		"total":   stats.TotalConns,
		"idle":    stats.IdleConns,
		"pending": stats.PendingRequests,
	} {
		output <- prometheus.MustNewConstMetric(
			collector.redisConnections,
			prometheus.GaugeValue,
			float64(value),
			state,
		)
	}
	for event, value := range map[string]uint32{
		"hit":      stats.Hits,
		"miss":     stats.Misses,
		"timeout":  stats.Timeouts,
		"wait":     stats.WaitCount,
		"unusable": stats.Unusable,
		"stale":    stats.StaleConns,
	} {
		output <- prometheus.MustNewConstMetric(
			collector.redisPoolEvents,
			prometheus.CounterValue,
			float64(value),
			event,
		)
	}
	output <- prometheus.MustNewConstMetric(
		collector.redisWaitTime,
		prometheus.CounterValue,
		float64(stats.WaitDurationNs)/1e9,
	)
}
