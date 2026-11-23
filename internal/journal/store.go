package journal

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// SQLite-created WAL/SHM files inherit the database's restrictive permissions.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection serializes writers and keeps PRAGMA configuration consistent.
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=30000; PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA synchronous=FULL;
 CREATE TABLE IF NOT EXISTS events(seq INTEGER PRIMARY KEY,id TEXT UNIQUE NOT NULL,journal_id TEXT NOT NULL,entry_id TEXT NOT NULL,kind TEXT NOT NULL,recorded_at TEXT NOT NULL,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT UNIQUE NOT NULL);
 CREATE TABLE IF NOT EXISTS outbox(seq INTEGER PRIMARY KEY REFERENCES events(seq),archived_at TEXT,generation TEXT,error TEXT);
 CREATE TABLE IF NOT EXISTS config(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS event_entry ON events(entry_id,seq);
 CREATE TRIGGER IF NOT EXISTS events_no_update BEFORE UPDATE ON events BEGIN SELECT RAISE(ABORT,'Journal events are immutable'); END;
 CREATE TRIGGER IF NOT EXISTS events_no_delete BEFORE DELETE ON events BEGIN SELECT RAISE(ABORT,'Journal events cannot be deleted'); END;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec("INSERT OR IGNORE INTO config VALUES('journal_id',?)", newID()); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) JournalID() (string, error) {
	var id string
	err := s.db.QueryRow("SELECT value FROM config WHERE key='journal_id'").Scan(&id)
	return id, err
}

type scanner interface{ Scan(...any) error }

func scanEvent(row scanner) (Event, error) {
	var e Event
	var payload string
	err := row.Scan(&e.Seq, &e.ID, &e.JournalID, &e.EntryID, &e.Kind, &e.RecordedAt, &e.PreviousHash, &payload, &e.Hash)
	if err != nil {
		return e, err
	}
	err = decode([]byte(payload), &e.Payload)
	return e, err
}
func insertEvent(tx *sql.Tx, e Event) error {
	body, err := Canonical(e.Payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO events(seq,id,journal_id,entry_id,kind,recorded_at,previous_hash,payload,hash) VALUES(?,?,?,?,?,?,?,?,?)`, e.Seq, e.ID, e.JournalID, e.EntryID, e.Kind, e.RecordedAt, e.PreviousHash, string(body), e.Hash)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO outbox(seq) VALUES(?)", e.Seq)
	return err
}
func (s *Store) append(kind string, payload Object, entryID string) (Event, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	if entryID == "" {
		entryID = newID()
	}
	if kind != "entry" {
		var n int
		if err = tx.QueryRow("SELECT COUNT(*) FROM events WHERE entry_id=? AND kind='entry'", entryID).Scan(&n); err != nil {
			return Event{}, err
		}
		if n == 0 {
			return Event{}, errors.New("entry does not exist")
		}
	}
	e := Event{ID: newID(), EntryID: entryID, Kind: kind, RecordedAt: timestamp(), PreviousHash: zeroHash, Payload: payload}
	var seq int64
	var hash string
	err = tx.QueryRow("SELECT seq,hash FROM events ORDER BY seq DESC LIMIT 1").Scan(&seq, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return e, err
	}
	if err == nil {
		e.PreviousHash = hash
	}
	e.Seq = seq + 1
	if err = tx.QueryRow("SELECT value FROM config WHERE key='journal_id'").Scan(&e.JournalID); err != nil {
		return e, err
	}
	if e.Hash, err = eventHash(e); err != nil {
		return e, err
	}
	if err = insertEvent(tx, e); err != nil {
		return e, err
	}
	return e, tx.Commit()
}
func (s *Store) Create(data Object) (Event, error) {
	d, err := Validate(data)
	if err != nil {
		return Event{}, err
	}
	return s.append("entry", d, "")
}
func (s *Store) Correct(id string, data Object, reason string) (Event, error) {
	if err := required(Object{"reason": reason}, "reason"); err != nil {
		return Event{}, err
	}
	d, err := Validate(data)
	if err != nil {
		return Event{}, err
	}
	return s.append("correction", Object{"reason": strings.TrimSpace(reason), "entry": d}, id)
}
func (s *Store) Attach(id, name, mime string, data []byte) (Event, error) {
	if len(data) == 0 || len(data) > MaxAttachment {
		return Event{}, errors.New("attachment must be between 1 byte and 64 MiB")
	}
	switch mime {
	case "application/pdf", "image/jpeg", "image/png", "video/mp4", "audio/mpeg", "audio/mp4":
	default:
		return Event{}, errors.New("use PDF, PNG, JPEG, MP4, M4A or MP3")
	}
	if err := required(Object{"name": name}, "name"); err != nil {
		return Event{}, err
	}
	return s.append("attachment", Object{"name": filepath.Base(name), "mime": mime, "sha256": checksum(data), "size": len(data), "data": base64.StdEncoding.EncodeToString(data)}, id)
}
func (s *Store) events(query string, args ...any) ([]Event, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Event, 0)
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
func (s *Store) AllEvents() ([]Event, error) { return s.events("SELECT * FROM events ORDER BY seq") }
func (s *Store) History(id string) ([]Event, error) {
	events, err := s.events("SELECT * FROM events WHERE entry_id=? ORDER BY seq", id)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		if e.Kind == "attachment" {
			delete(e.Payload, "data")
		}
	}
	return events, nil
}
func (s *Store) Attachment(seq int64) (Object, error) {
	e, err := scanEvent(s.db.QueryRow("SELECT * FROM events WHERE seq=? AND kind='attachment'", seq))
	return e.Payload, err
}

type Entry struct {
	ID              string `json:"id"`
	CreatedAt       string `json:"created_at"`
	Corrections     int    `json:"corrections"`
	Data            Object `json:"data"`
	Archived        bool   `json:"archived"`
	AttachmentCount int    `json:"attachment_count"`
}

func (s *Store) Entries() ([]Entry, error) {
	events, err := s.events("SELECT * FROM events WHERE kind!='attachment' ORDER BY seq")
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	result := make([]Entry, 0)
	for _, e := range events {
		if e.Kind == "entry" {
			index[e.EntryID] = len(result)
			result = append(result, Entry{ID: e.EntryID, CreatedAt: e.RecordedAt, Data: e.Payload, Archived: true})
		} else {
			i, ok := index[e.EntryID]
			if !ok {
				return nil, errors.New("invalid correction relationship")
			}
			d, ok := e.Payload["entry"].(map[string]any)
			if !ok {
				return nil, errors.New("invalid correction")
			}
			result[i].Data = d
			result[i].Corrections++
		}
	}
	rows, err := s.db.Query(`SELECT e.entry_id,SUM(e.kind='attachment'),SUM(o.archived_at IS NULL) FROM events e JOIN outbox o USING(seq) GROUP BY e.entry_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var count, pending int
		if err := rows.Scan(&id, &count, &pending); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			result[i].AttachmentCount = count
			result[i].Archived = pending == 0
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for a, b := 0, len(result)-1; a < b; a, b = a+1, b-1 {
		result[a], result[b] = result[b], result[a]
	}
	return result, nil
}

func VerifyEvents(events []Event) (Integrity, error) {
	out := Integrity{Events: len(events), Head: zeroHash}
	ids := map[string]bool{}
	entries := map[string]bool{}
	for i, e := range events {
		hash, err := eventHash(e)
		if err != nil || e.Seq != int64(i+1) || e.PreviousHash != out.Head || hash != e.Hash {
			return out, fmt.Errorf("integrity check failed at event %d", i+1)
		}
		if e.JournalID == "" || e.ID == "" || e.EntryID == "" || ids[e.ID] || out.JournalID != "" && out.JournalID != e.JournalID {
			return out, errors.New("mixed journal, invalid identity or duplicate event")
		}
		ids[e.ID] = true
		out.JournalID = e.JournalID
		switch e.Kind {
		case "entry":
			if entries[e.EntryID] {
				return out, errors.New("duplicate entry")
			}
			entries[e.EntryID] = true
		case "correction", "attachment":
			if !entries[e.EntryID] {
				return out, errors.New("invalid event relationship")
			}
		default:
			return out, errors.New("invalid event kind")
		}
		if e.Kind == "attachment" {
			data, err := base64.StdEncoding.Strict().DecodeString(stringValue(e.Payload, "data"))
			size, ok := integer(e.Payload["size"])
			if err != nil || !ok || size != int64(len(data)) || checksum(data) != stringValue(e.Payload, "sha256") {
				return out, errors.New("attachment checksum failed")
			}
		}
		out.Head = e.Hash
	}
	return out, nil
}
func (s *Store) Verify() (Integrity, error) {
	events, err := s.AllEvents()
	if err != nil {
		return Integrity{}, err
	}
	return VerifyEvents(events)
}
func (s *Store) Restore(events []Event) (Integrity, error) {
	result, err := VerifyEvents(events)
	if err != nil {
		return result, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRow("SELECT COUNT(*) FROM events").Scan(&n); err != nil {
		return result, err
	}
	if n != 0 {
		return result, errors.New("restore requires an empty journal database")
	}
	if result.JournalID != "" {
		if _, err = tx.Exec("UPDATE config SET value=? WHERE key='journal_id'", result.JournalID); err != nil {
			return result, err
		}
	}
	for _, e := range events {
		if err = insertEvent(tx, e); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}

type Status struct {
	Total         int     `json:"total"`
	Pending       int     `json:"pending"`
	LastArchive   *string `json:"last_archive"`
	LastError     *string `json:"last_error"`
	JournalID     string  `json:"journal_id"`
	GCSConfigured bool    `json:"gcs_configured"`
	ArchiveError  *string `json:"archive_error"`
}

func (s *Store) Status() (Status, error) {
	var st Status
	err := s.db.QueryRow("SELECT COUNT(*),COALESCE(SUM(archived_at IS NULL),0),MAX(archived_at) FROM outbox").Scan(&st.Total, &st.Pending, &st.LastArchive)
	if err != nil {
		return st, err
	}
	err = s.db.QueryRow("SELECT error FROM outbox WHERE error IS NOT NULL ORDER BY seq DESC LIMIT 1").Scan(&st.LastError)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	st.JournalID, err = s.JournalID()
	return st, err
}
func (s *Store) NextPending() (Event, error) {
	return scanEvent(s.db.QueryRow("SELECT e.* FROM events e JOIN outbox o USING(seq) WHERE o.archived_at IS NULL ORDER BY seq LIMIT 1"))
}
func (s *Store) markArchived(seq int64, generation string) error {
	_, err := s.db.Exec("UPDATE outbox SET archived_at=?,generation=?,error=NULL WHERE seq=?", timestamp(), generation, seq)
	return err
}
func (s *Store) archiveFailed(message string) error {
	_, err := s.db.Exec("UPDATE outbox SET error=? WHERE seq=(SELECT MIN(seq) FROM outbox WHERE archived_at IS NULL)", message)
	return err
}
func (s *Store) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }
