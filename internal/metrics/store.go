package metrics

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// MetricsRecord holds resource usage data for a single completed job run.
type MetricsRecord struct {
	JobName              string
	Profile              string
	CPUAllocatedNanoCPUs int64
	MemAllocatedBytes    int64
	CPUUsedNanoCPUs      int64
	MemPeakBytes         int64
	DurationSec          float64
}

// Store is a SQLite-backed metrics store for job resource usage history.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) a SQLite database at dbPath and initialises the schema.
// Pass ":memory:" for an in-process test database.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening metrics db: %w", err)
	}
	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Record inserts a MetricsRecord into the store.
func (s *Store) Record(r *MetricsRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO job_metrics (job_name, profile, cpu_allocated_nanocpus, mem_allocated_bytes, cpu_used_nanocpus, mem_peak_bytes, duration_sec)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.JobName, r.Profile, r.CPUAllocatedNanoCPUs, r.MemAllocatedBytes, r.CPUUsedNanoCPUs, r.MemPeakBytes, r.DurationSec,
	)
	if err != nil {
		return fmt.Errorf("inserting metrics: %w", err)
	}
	return nil
}

// GetHistory returns up to limit recent MetricsRecords for the given jobName,
// ordered most-recent first.
func (s *Store) GetHistory(jobName string, limit int) ([]MetricsRecord, error) {
	rows, err := s.db.Query(`
		SELECT job_name, profile, cpu_allocated_nanocpus, mem_allocated_bytes, cpu_used_nanocpus, mem_peak_bytes, duration_sec
		FROM job_metrics
		WHERE job_name = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`,
		jobName, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying metrics: %w", err)
	}
	defer rows.Close()

	var records []MetricsRecord
	for rows.Next() {
		var r MetricsRecord
		if err := rows.Scan(&r.JobName, &r.Profile, &r.CPUAllocatedNanoCPUs, &r.MemAllocatedBytes, &r.CPUUsedNanoCPUs, &r.MemPeakBytes, &r.DurationSec); err != nil {
			return nil, fmt.Errorf("scanning metrics row: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// createSchema creates the job_metrics table and supporting index if they do not exist.
func createSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS job_metrics (
			id                      INTEGER PRIMARY KEY AUTOINCREMENT,
			job_name                TEXT NOT NULL,
			profile                 TEXT NOT NULL,
			cpu_allocated_nanocpus  INTEGER,
			mem_allocated_bytes     INTEGER,
			cpu_used_nanocpus       INTEGER,
			mem_peak_bytes          INTEGER,
			duration_sec            REAL,
			created_at              TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_job_name ON job_metrics(job_name);
	`)
	if err != nil {
		return fmt.Errorf("creating schema: %w", err)
	}
	return nil
}
