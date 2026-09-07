// db — merged CLI source: db.go, db_migrate.go, db_create_table.go,
// db_check_rls.go.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/spf13/cobra"

	"github.com/Timwood0x10/ares/internal/storage/postgres"
)

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Database management commands",
	Long: `Manage ARES databases: migrate, setup test databases,
create specific tables, and inspect RLS policies.`,
}

// The init blocks below (db root / migrate / create-table / check-rls) were
// one per former file and only AddCommand into disjoint trees — they share no
// state and do not depend on execution order (cobra also lists subcommands
// alphabetically, so the relative order flip from the merge is invisible).
func init() {
	rootCmd.AddCommand(dbCmd)
}

var dbMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Run full database migration",
	Long: `Creates the database if it doesn't exist and runs all migrations.
Reads DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME env vars.
Default: postgres://postgres:postgres@localhost:5432/ARES?sslmode=disable`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDbMigrate()
	},
}

func init() {
	dbCmd.AddCommand(dbMigrateCmd)
}

func runDbMigrate() error {
	host := getEnv("DB_HOST", "localhost")
	port := getEnv("DB_PORT", "5432")
	user := getEnv("DB_USER", "postgres")
	password := getEnv("DB_PASSWORD", "postgres")
	dbname := getEnv("DB_NAME", "ARES")

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		url.QueryEscape(user), url.QueryEscape(password),
		host, port, dbname)

	parsed, _ := url.Parse(dsn)
	dbname = strings.TrimPrefix(parsed.Path, "/")
	portStr := parsed.Port()

	adminDB := connectAdmin(changeDB(dsn, "postgres"))
	ensureDatabase(adminDB, dbname)
	if err := adminDB.Close(); err != nil {
		return fmt.Errorf("close admin connection: %w", err)
	}

	cfg := &postgres.Config{
		Host:            parsed.Hostname(),
		Port:            parsePort(portStr, 5432),
		User:            parsed.User.Username(),
		Password:        passwordFromURL(parsed),
		Database:        dbname,
		SSLMode:         getEnv("DB_SSL_MODE", "disable"),
		MaxOpenConns:    25,
		MaxIdleConns:    10,
		ConnMaxLifetime: 0,
		ConnMaxIdleTime: 0,
		QueryTimeout:    30 * time.Second,
	}

	pool, err := postgres.NewPool(cfg)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "close pool: %v\n", err)
		}
	}()

	ctx := context.Background()

	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("enable pgvector: %w", err)
	}
	fmt.Println("pgvector extension enabled")

	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("core migration: %w", err)
	}
	fmt.Println("Core application tables migrated (sessions, agent_checkpoints, events, ...)")

	if err := postgres.MigrateStorage(ctx, pool); err != nil {
		return fmt.Errorf("migration: %w", err)
	}
	fmt.Println("Production database migrations completed successfully")
	fmt.Println()
	fmt.Println("Tables created:")
	fmt.Println("  - knowledge_chunks_1024")
	fmt.Println("  - experiences_1024")
	fmt.Println("  - tools")
	fmt.Println("  - conversations")
	fmt.Println("  - task_results_1024")
	fmt.Println("  - secrets")
	fmt.Println("  - embedding_queue")
	fmt.Println("  - embedding_dead_letter")
	fmt.Println("  - distilled_memories")

	return nil
}

func connectAdmin(dsn string) *sql.DB {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to postgres: %v\n", err)
		os.Exit(1)
	}
	if err := db.PingContext(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "failed to ping postgres: %v\n", err)
		os.Exit(1)
	}
	return db
}

func ensureDatabase(db *sql.DB, name string) {
	var exists bool
	if err := db.QueryRowContext(context.Background(), "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		fmt.Fprintf(os.Stderr, "failed to check database existence: %v\n", err)
		os.Exit(1)
	}
	if !exists {
		if _, err := db.ExecContext(context.Background(), fmt.Sprintf("CREATE DATABASE %s", pqQuoteIdent(name))); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create database: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created database: %s\n", name)
	}
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func changeDB(dsn, dbname string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + dbname
	return u.String()
}

func parsePort(port string, defaultPort int) int {
	if port == "" {
		return defaultPort
	}
	var p int
	if _, err := fmt.Sscanf(port, "%d", &p); err != nil || p <= 0 {
		return defaultPort
	}
	return p
}

func passwordFromURL(u *url.URL) string {
	if pw, ok := u.User.Password(); ok {
		return pw
	}
	return ""
}

func pqQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

var dbCreateTableCmd = &cobra.Command{
	Use:   "create-table",
	Short: "Create distilled_memories table",
	Long: `Creates the distilled_memories table with indexes and RLS.
Env vars: DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME.
Default: postgres://postgres:postgres@localhost:5432/ARES?sslmode=disable`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDbCreateTable()
	},
}

func init() {
	dbCmd.AddCommand(dbCreateTableCmd)
}

func runDbCreateTable() error {
	host := getEnv("DB_HOST", "localhost")
	port := getEnv("DB_PORT", "5433")
	user := getEnv("DB_USER", "postgres")
	password := getEnv("DB_PASSWORD", "postgres")
	dbname := getEnv("DB_NAME", "ARES")

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		url.QueryEscape(user), url.QueryEscape(password),
		host, port, dbname)

	parsed, _ := url.Parse(dsn)
	dbname = strings.TrimPrefix(parsed.Path, "/")

	adminDB := connectAdmin(changeDB(dsn, "postgres"))

	ensureDatabase(adminDB, dbname)
	if err := adminDB.Close(); err != nil {
		return fmt.Errorf("close admin db: %w", err)
	}

	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = pool.Close() }()

	ctx := context.Background()

	createTableSQL := `
		CREATE TABLE IF NOT EXISTS distilled_memories (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id TEXT NOT NULL,
			user_id TEXT,
			session_id TEXT,
			content TEXT NOT NULL,
			embedding VECTOR(1024),
			embedding_model TEXT NOT NULL DEFAULT 'intfloat/e5-large',
			embedding_version INT NOT NULL DEFAULT 1,
			memory_type VARCHAR(50) DEFAULT 'profile',
			importance FLOAT DEFAULT 0.5,
			metadata JSONB DEFAULT '{}'::jsonb,
			access_count INTEGER DEFAULT 0,
			last_accessed_at TIMESTAMP,
			expires_at TIMESTAMP DEFAULT NOW() + INTERVAL '90 days',
			created_at TIMESTAMP DEFAULT NOW()
		)
	`
	if _, err := pool.ExecContext(ctx, createTableSQL); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	fmt.Println("distilled_memories table created successfully")

	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_distilled_memories_tenant ON distilled_memories(tenant_id)`,
		`CREATE INDEX IF NOT EXISTS idx_distilled_memories_user ON distilled_memories(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_distilled_memories_session ON distilled_memories(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_distilled_memories_type ON distilled_memories(memory_type)`,
		`CREATE INDEX IF NOT EXISTS idx_distilled_memories_expires ON distilled_memories(expires_at) WHERE expires_at IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_distilled_memories_importance ON distilled_memories(importance DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_distilled_memories_embedding ON distilled_memories USING ivfflat (embedding vector_cosine_ops) WHERE embedding IS NOT NULL`,
	}
	for _, idx := range indexes {
		if _, err := pool.ExecContext(ctx, idx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to create index: %s\n  error: %v\n", idx, err)
		}
	}
	fmt.Println("Indexes created successfully")

	if _, err := pool.ExecContext(ctx, `ALTER TABLE distilled_memories ENABLE ROW LEVEL SECURITY`); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to enable RLS: %v\n", err)
	}
	// PostgreSQL has no CREATE POLICY IF NOT EXISTS; DROP+CREATE inside a DO
	// block is idempotent and atomic (runs in one transaction).
	rlsSQL := `DO $$
BEGIN
    DROP POLICY IF EXISTS tenant_isolation_distilled_memories ON distilled_memories;
    CREATE POLICY tenant_isolation_distilled_memories ON distilled_memories USING (tenant_id = current_setting('app.tenant_id', true));
END $$;`
	if _, err := pool.ExecContext(ctx, rlsSQL); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to create RLS policy: %v\n", err)
	} else {
		fmt.Println("Row Level Security enabled")
	}

	return nil
}

// errStdout is used by check-rls for warning messages.
var errStdout = os.Stderr

var dbCheckRLSCmd = &cobra.Command{
	Use:   "check-rls",
	Short: "Check RLS policies on distilled_memories table",
	Long: `Inspects Row-Level Security policies and column structure
of the distilled_memories table.
Env vars: DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME.
Default: postgres://postgres:postgres@localhost:5432/ARES?sslmode=disable`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDbCheckRLS()
	},
}

func init() {
	dbCmd.AddCommand(dbCheckRLSCmd)
}

func runDbCheckRLS() error {
	dbConfig := &postgres.Config{
		Host:            "127.0.0.1",
		Port:            5433,
		User:            "postgres",
		Password:        "postgres",
		Database:        "ARES",
		MaxOpenConns:    25,
		MaxIdleConns:    10,
		ConnMaxLifetime: 5 * time.Minute,
		QueryTimeout:    30 * time.Second,
		Embedding:       postgres.DefaultEmbeddingConfig(),
	}

	pool, err := postgres.NewPool(dbConfig)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			_, _ = fmt.Fprintf(errStdout, "warning: close pool: %v\n", err)
		}
	}()

	ctx := context.Background()

	fmt.Println("=== RLS Policies for distilled_memories ===")
	rows, err := pool.Query(ctx, `
		SELECT schemaname, tablename, policyname, permissive, roles, cmd, qual, with_check
		FROM pg_policies
		WHERE tablename = 'distilled_memories'
	`)
	if err != nil {
		return fmt.Errorf("query RLS policies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hasPolicies := false
	for rows.Next() {
		hasPolicies = true
		var schema, table, policyName, roles, cmd, qual, withCheck string
		var permissive bool
		if err := rows.Scan(&schema, &table, &policyName, &permissive, &roles, &cmd, &qual, &withCheck); err != nil {
			_, _ = fmt.Fprintf(errStdout, "scan policy: %v\n", err)
			continue
		}
		fmt.Printf("\nPolicy: %s\n", policyName)
		fmt.Printf("  Schema: %s\n", schema)
		fmt.Printf("  Table: %s\n", table)
		fmt.Printf("  Permissive: %v\n", permissive)
		fmt.Printf("  Roles: %s\n", roles)
		fmt.Printf("  Command: %s\n", cmd)
		fmt.Printf("  Qual: %s\n", qual)
		fmt.Printf("  With Check: %s\n", withCheck)
	}

	if !hasPolicies {
		fmt.Println("No RLS policies found")
	}

	fmt.Println("\n=== Table Structure ===")
	rows2, err := pool.Query(ctx, `
		SELECT column_name, data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_name = 'distilled_memories'
		ORDER BY ordinal_position
	`)
	if err != nil {
		return fmt.Errorf("query table structure: %w", err)
	}
	defer func() { _ = rows2.Close() }()

	for rows2.Next() {
		var colName, dataType, nullable, defaultVal string
		if err := rows2.Scan(&colName, &dataType, &nullable, &defaultVal); err != nil {
			_, _ = fmt.Fprintf(errStdout, "scan column: %v\n", err)
			continue
		}
		fmt.Printf("  %-20s %-20s %-8s %s\n", colName, dataType, nullable, defaultVal)
	}

	return nil
}
