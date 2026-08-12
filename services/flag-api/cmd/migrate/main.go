// Command migrate applies the flag-api database migrations.
//
//	go run ./cmd/migrate                 # apply all pending migrations
//	go run ./cmd/migrate -baseline       # adopt the runner on a hand-built DB
//	                                       (record versions as applied, run no SQL)
//
// DB_URL must point at the target Postgres. Intended for CI, docker-compose
// init, and ops — flag-api's own startup does NOT auto-migrate (schema changes
// stay an explicit, auditable step).
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"

	"github.com/tombstone/flag-api/internal/db"
)

func main() {
	baseline := flag.Bool("baseline", false, "record all versions as applied without running SQL (for adopting the runner on an existing hand-built DB)")
	flag.Parse()

	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		log.Fatal("DB_URL environment variable is required")
	}

	database, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := database.PingContext(ctx); err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	if *baseline {
		recorded, err := db.Baseline(ctx, database)
		if err != nil {
			log.Fatalf("baseline failed: %v", err)
		}
		log.Printf("baseline complete: recorded %d version(s) as applied: %v", len(recorded), recorded)
		return
	}

	applied, err := db.Migrate(ctx, database)
	if err != nil {
		log.Fatalf("migrate failed: %v", err)
	}
	if len(applied) == 0 {
		log.Println("database is up to date — no migrations applied")
		return
	}
	log.Printf("applied %d migration(s): %v", len(applied), applied)
}
