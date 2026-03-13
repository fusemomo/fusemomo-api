package db

import (
	"context"
	"log"
	"sync"
	"time"

	"fusemomo-api/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	pool *pgxpool.Pool
	once sync.Once
)

func GetPool() *pgxpool.Pool {
	once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		poolConfig, err := pgxpool.ParseConfig(config.Envs.DATABASE_URL)
		if err != nil {
			log.Fatalf("Unable to parse DATABASE_URL: %v\n", err)
		}

		// Disable prepared statement cache to fix PgBouncer (Supabase) transaction pooling collisions
		poolConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

		pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			log.Fatalf("Unable to connect to database: %v\n", err)
		}

		if err := pool.Ping(ctx); err != nil {
			log.Fatalf("Unable to ping database: %v\n", err)
		}

		log.Println("Connected to database")
	})

	return pool
}

func Close() {
	if pool != nil {
		pool.Close()
	}
}
