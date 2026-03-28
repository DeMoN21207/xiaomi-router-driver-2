package events

import (
	"database/sql"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const maxEvents = 1000

type Event struct {
	ID         string `json:"id"`
	Level      string `json:"level"`
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	OccurredAt string `json:"occurredAt"`
}

type Store struct {
	db          *sql.DB
	legacyPath  string
	mu          sync.Mutex
	initialized bool
	initErr     error
}

func NewStore(db *sql.DB, legacyPath string) *Store {
	return &Store{
		db:         db,
		legacyPath: strings.TrimSpace(legacyPath),
	}
}

func (s *Store) Add(level string, kind string, message string) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureReadyLocked(); err != nil {
		return Event{}, err
	}

	event := Event{
		ID:         newEventID(),
		Level:      level,
		Kind:       kind,
		Message:    message,
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Event{}, err
	}

	if _, err := tx.Exec(`
		INSERT INTO events (id, level, kind, message, occurred_at)
		VALUES (?, ?, ?, ?, ?)
	`, event.ID, event.Level, event.Kind, event.Message, event.OccurredAt); err != nil {
		_ = tx.Rollback()
		return Event{}, err
	}

	if _, err := tx.Exec(`
		DELETE FROM events
		WHERE id IN (
			SELECT id
			FROM events
			ORDER BY occurred_at DESC, id DESC
			LIMIT -1 OFFSET ?
		)
	`, maxEvents); err != nil {
		_ = tx.Rollback()
		return Event{}, err
	}

	if err := tx.Commit(); err != nil {
		return Event{}, err
	}

	return event, nil
}

func (s *Store) List(limit, offset int) ([]Event, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureReadyLocked(); err != nil {
		return nil, 0, err
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM events`).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `
		SELECT id, level, kind, message, occurred_at
		FROM events
		ORDER BY occurred_at DESC, id DESC
	`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	if offset > 0 {
		query += ` OFFSET ?`
		args = append(args, offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	events := make([]Event, 0, 16)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Level, &event.Kind, &event.Message, &event.OccurredAt); err != nil {
			return nil, 0, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return events, total, nil
}

func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureReadyLocked(); err != nil {
		return err
	}

	_, err := s.db.Exec(`DELETE FROM events`)
	return err
}

func (s *Store) ensureReadyLocked() error {
	if s.initialized {
		return s.initErr
	}
	s.initialized = true

	if s.db == nil {
		s.initErr = errors.New("events database is not configured")
		return s.initErr
	}

	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id TEXT PRIMARY KEY,
			level TEXT NOT NULL,
			kind TEXT NOT NULL,
			message TEXT NOT NULL,
			occurred_at TEXT NOT NULL
		)
	`); err != nil {
		s.initErr = err
		return err
	}

	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_events_occurred_at ON events(occurred_at DESC)`); err != nil {
		s.initErr = err
		return err
	}

	if err := s.migrateLegacyLocked(); err != nil {
		s.initErr = err
		return err
	}

	return nil
}

func (s *Store) migrateLegacyLocked() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM events`).Scan(&count); err != nil || count > 0 {
		return err
	}

	events, err := loadLegacyEvents(s.legacyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	for _, event := range events {
		if _, err := tx.Exec(`
			INSERT INTO events (id, level, kind, message, occurred_at)
			VALUES (?, ?, ?, ?, ?)
		`, event.ID, event.Level, event.Kind, event.Message, event.OccurredAt); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

func loadLegacyEvents(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if len(strings.TrimSpace(string(data))) == 0 {
		return []Event{}, nil
	}

	var events []Event
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, err
	}

	return events, nil
}

func newEventID() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	return "evt_" + hex.EncodeToString(buf)
}
