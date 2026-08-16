package machines

import (
	"database/sql"
	"os"
	"runtime"
	"time"

	"github.com/google/uuid"
)

type Machine struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Status         string         `json:"status"`
	OS             string         `json:"os"`
	Architecture   string         `json:"architecture"`
	Hostname       string         `json:"hostname"`
	DockerVersion  string         `json:"dockerVersion"`
	ComposeVersion string         `json:"composeVersion"`
	Resources      map[string]any `json:"resources"`
}
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }
func (s *Store) EnsureLocal() (Machine, error) {
	hostname, _ := os.Hostname()
	var id string
	err := s.db.QueryRow(`SELECT id FROM machines WHERE mode='local' LIMIT 1`).Scan(&id)
	if err == nil {
		return s.Get(id)
	}
	id = uuid.NewString()
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO machines(id,name,mode,status,last_heartbeat_at,os,architecture,hostname,agent_version,resources_json,created_at,updated_at) VALUES(?,?, 'local','online',?,?,?,?, 'development','{}',?,?)`, id, hostname, now, runtime.GOOS, runtime.GOARCH, hostname, now, now)
	if err != nil {
		return Machine{}, err
	}
	return s.Get(id)
}
func (s *Store) Get(id string) (Machine, error) {
	var m Machine
	var r string
	err := s.db.QueryRow(`SELECT id,name,status,os,architecture,hostname,COALESCE(docker_version,''),COALESCE(compose_version,''),resources_json FROM machines WHERE id=?`, id).Scan(&m.ID, &m.Name, &m.Status, &m.OS, &m.Architecture, &m.Hostname, &m.DockerVersion, &m.ComposeVersion, &r)
	m.Resources = map[string]any{"reported": r}
	return m, err
}
func (s *Store) List() ([]Machine, error) {
	rows, err := s.db.Query(`SELECT id FROM machines ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Machine
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		m, e := s.Get(id)
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
