package apps

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Application struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	MachineName string    `json:"machineName"`
	CreatedAt   time.Time `json:"createdAt"`
}
type Service struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Port   *int   `json:"port,omitempty"`
}
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func (s *Store) Create(name, description, sourcePath, machineID string) (Application, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Application{}, errors.New("name is required")
	}
	slug := slugify(name)
	if slug == "" {
		return Application{}, errors.New("name must include letters or numbers")
	}
	id := uuid.NewString()
	if machineID == "" {
		_ = s.db.QueryRow(`SELECT id FROM machines WHERE mode='local' LIMIT 1`).Scan(&machineID)
	}
	now := s.now().UTC()
	_, err := s.db.Exec(`INSERT INTO applications(id,slug,name,description,source_path,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?,?,?, 'draft',?,?)`, id, slug, name, description, sourcePath, machineID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Application{}, err
	}
	return s.Get(id)
}
func (s *Store) List() ([]Application, error) {
	rows, err := s.db.Query(`SELECT a.id,a.slug,a.name,a.description,a.status,COALESCE(m.name,''),a.created_at FROM applications a LEFT JOIN machines m ON m.id=a.active_machine_id WHERE a.archived_at IS NULL ORDER BY a.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) Get(id string) (Application, error) {
	return scan(s.db.QueryRow(`SELECT a.id,a.slug,a.name,a.description,a.status,COALESCE(m.name,''),a.created_at FROM applications a LEFT JOIN machines m ON m.id=a.active_machine_id WHERE a.id=? AND a.archived_at IS NULL`, id))
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Application, error) {
	var a Application
	var created string
	err := row.Scan(&a.ID, &a.Slug, &a.Name, &a.Description, &a.Status, &a.MachineName, &created)
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return a, err
}
func (s *Store) Services(appID string) ([]Service, error) {
	rows, err := s.db.Query(`SELECT id,name,kind,'Unknown',internal_port FROM services WHERE app_id=? ORDER BY name`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Service{}
	for rows.Next() {
		var v Service
		if err = rows.Scan(&v.ID, &v.Name, &v.Kind, &v.Status, &v.Port); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Inspect(source string) (map[string]any, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, errors.New("source path is required")
	}
	return map[string]any{"source": source, "inspection": "not_run", "message": "Compose inspection is available in Milestone 2; this app can be saved as a Phase A draft."}, nil
}
