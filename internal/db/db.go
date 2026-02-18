package db

import (
	"context"
	"log"
	"sync"

	"fusemomo-api/internal/config"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	pool *pgxpool.Pool
	once sync.Once
)

func GetPool() *pgxpool.Pool {
	once.Do(func() {
		var err error
		pool, err = pgxpool.New(context.Background(), config.Envs.DATABASE_URL)
		if err != nil {
			log.Fatalf("Unable to connect to database: %v\n", err)
		}

		if err := pool.Ping(context.Background()); err != nil {
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
