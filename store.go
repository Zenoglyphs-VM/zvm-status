package main

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// Store keeps every observation for the retention window, and the incidents
// derived from them. 30 days at one check per component every five minutes is
// about 8,600 rows a component: small enough that every summary is computed
// from the raw rows on request, with nothing rolled up that could disagree
// with them.
type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	// WAL so the API can read while a round is being written.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS checks (
  component TEXT NOT NULL,
  ts        INTEGER NOT NULL,  -- unix seconds
  status    INTEGER NOT NULL,  -- 0 operational, 1 degraded, 2 down
  latency   INTEGER NOT NULL,  -- milliseconds
  detail    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS checks_by_component ON checks (component, ts);

-- One row per episode of a component being anything but operational. It is
-- opened on the first bad check and closed on the first good one, and "worst"
-- is what decided whether anyone was emailed.
CREATE TABLE IF NOT EXISTS incidents (
  id        INTEGER PRIMARY KEY,
  component TEXT NOT NULL,
  started   INTEGER NOT NULL,
  resolved  INTEGER,
  worst     INTEGER NOT NULL,
  detail    TEXT NOT NULL,
  alerted   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS incidents_by_start ON incidents (started);
`)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Record(component string, at time.Time, r Result) error {
	_, err := s.db.Exec(`INSERT INTO checks VALUES (?, ?, ?, ?, ?)`,
		component, at.Unix(), int(r.Status), r.Latency.Milliseconds(), r.Detail)
	return err
}

type Incident struct {
	ID        int64      `json:"id"`
	Component string     `json:"component"`
	Started   time.Time  `json:"started"`
	Resolved  *time.Time `json:"resolved,omitempty"`
	Worst     Status     `json:"-"`
	Detail    string     `json:"detail"`
	Alerted   bool       `json:"-"`
}

// Open returns the unresolved incident for a component, if there is one.
func (s *Store) Open(component string) (*Incident, error) {
	row := s.db.QueryRow(`SELECT id, started, worst, detail, alerted FROM incidents
		WHERE component = ? AND resolved IS NULL ORDER BY id DESC LIMIT 1`, component)
	var in Incident
	var started int64
	var alerted int
	err := row.Scan(&in.ID, &started, &in.Worst, &in.Detail, &alerted)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	in.Component, in.Started, in.Alerted = component, time.Unix(started, 0), alerted != 0
	return &in, nil
}

func (s *Store) OpenIncident(component string, at time.Time, r Result) (*Incident, error) {
	res, err := s.db.Exec(`INSERT INTO incidents (component, started, worst, detail) VALUES (?, ?, ?, ?)`,
		component, at.Unix(), int(r.Status), r.Detail)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Incident{ID: id, Component: component, Started: at, Worst: r.Status, Detail: r.Detail}, nil
}

func (s *Store) Escalate(id int64, r Result) error {
	_, err := s.db.Exec(`UPDATE incidents SET worst = ?, detail = ? WHERE id = ?`, int(r.Status), r.Detail, id)
	return err
}

func (s *Store) MarkAlerted(id int64) error {
	_, err := s.db.Exec(`UPDATE incidents SET alerted = 1 WHERE id = ?`, id)
	return err
}

func (s *Store) Resolve(id int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE incidents SET resolved = ? WHERE id = ?`, at.Unix(), id)
	return err
}

// Prune drops everything older than the window. An incident is kept while it
// is still open, however old, so a long outage never loses its start.
func (s *Store) Prune(before time.Time) error {
	if _, err := s.db.Exec(`DELETE FROM checks WHERE ts < ?`, before.Unix()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM incidents WHERE resolved IS NOT NULL AND resolved < ?`, before.Unix())
	return err
}

type Check struct {
	At      time.Time
	Status  Status
	Latency int64
	Detail  string
}

func (s *Store) Since(component string, since time.Time) ([]Check, error) {
	rows, err := s.db.Query(`SELECT ts, status, latency, detail FROM checks
		WHERE component = ? AND ts >= ? ORDER BY ts`, component, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Check
	for rows.Next() {
		var c Check
		var ts int64
		if err := rows.Scan(&ts, &c.Status, &c.Latency, &c.Detail); err != nil {
			return nil, err
		}
		c.At = time.Unix(ts, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Incidents(since time.Time) ([]Incident, error) {
	rows, err := s.db.Query(`SELECT id, component, started, resolved, worst, detail, alerted FROM incidents
		WHERE started >= ? OR resolved IS NULL ORDER BY started DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var in Incident
		var started int64
		var resolved sql.NullInt64
		var alerted int
		if err := rows.Scan(&in.ID, &in.Component, &started, &resolved, &in.Worst, &in.Detail, &alerted); err != nil {
			return nil, err
		}
		in.Started, in.Alerted = time.Unix(started, 0), alerted != 0
		if resolved.Valid {
			t := time.Unix(resolved.Int64, 0)
			in.Resolved = &t
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
