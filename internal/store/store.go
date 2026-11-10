package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the SQLite file on the data volume.
type Store struct {
	db   *sql.DB
	path string
}

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	ctx := context.Background()
	if err := s.tune(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) tune(ctx context.Context) error {
	for _, q := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlite %s: %w", q, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Chmod(mode os.FileMode) error {
	return os.Chmod(s.path, mode)
}

func (s *Store) SecretFP(ctx context.Context) (string, error) {
	var fp string
	err := s.db.QueryRowContext(ctx, `SELECT secret_fp FROM settings WHERE id = 1`).Scan(&fp)
	return fp, err
}

func (s *Store) SetSecretFP(ctx context.Context, fp string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE settings SET secret_fp = ?, updated_at = ? WHERE id = 1`, fp, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	type mig struct {
		version int
		name    string
	}
	var list []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := versionOf(e.Name())
		if err != nil {
			return err
		}
		list = append(list, mig{version: v, name: e.Name()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].version < list[j].version })
	for _, m := range list {
		applied, err := s.applied(ctx, m.version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + m.name)
		if err != nil {
			return err
		}
		if err := s.apply(ctx, m.version, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

func (s *Store) applied(ctx context.Context, version int) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return false, nil
		}
		return false, err
	}
	return n > 0, nil
}

func (s *Store) apply(ctx context.Context, version int, script string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range statements(script) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func versionOf(name string) (int, error) {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("migration %s has no version", name)
	}
	return strconv.Atoi(name[:i])
}

func statements(script string) []string {
	var out []string
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if strings.HasSuffix(trim, ";") {
			if s := strings.TrimSpace(b.String()); s != "" {
				out = append(out, s)
			}
			b.Reset()
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		out = append(out, s)
	}
	return out
}
