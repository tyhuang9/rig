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
	Source      Source    `json:"source"`
}

const (
	SourceLocal  = "local"
	SourceGitHub = "github"
)

type Source struct {
	Type            string
	Path            string
	ConnectionID    string
	InstallationID  int64
	RepositoryID    int64
	RepositoryOwner string
	RepositoryName  string
	TrackedBranch   string
	TrackedRef      string
	ComposePath     string
	ResolvedSHA     string
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
	return s.CreateWithSource(name, description, machineID, Source{Type: SourceLocal, Path: sourcePath})
}

func (s *Store) CreateWithSource(name, description, machineID string, source Source) (Application, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Application{}, errors.New("name is required")
	}
	slug := slugify(name)
	if slug == "" {
		return Application{}, errors.New("name must include letters or numbers")
	}
	if err := validateSource(&source); err != nil {
		return Application{}, err
	}
	id := uuid.NewString()
	if machineID == "" {
		_ = s.db.QueryRow(`SELECT id FROM machines WHERE mode='local' LIMIT 1`).Scan(&machineID)
	}
	now := s.now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return Application{}, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO applications(id,slug,name,description,source_path,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?,?,?, 'draft',?,?)`, id, slug, name, description, source.Path, machineID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Application{}, err
	}
	_, err = tx.Exec(`INSERT INTO application_sources(application_id,source_type,connection_id,installation_id,repository_id,repository_owner,repository_name,tracked_branch,tracked_ref,compose_path,resolved_sha,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, source.Type, nullable(source.ConnectionID), nullableInt64(source.InstallationID), nullableInt64(source.RepositoryID), nullable(source.RepositoryOwner), nullable(source.RepositoryName), nullable(source.TrackedBranch), nullable(source.TrackedRef), nullable(source.ComposePath), nullable(source.ResolvedSHA), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Application{}, err
	}
	if err := tx.Commit(); err != nil {
		return Application{}, err
	}
	return s.Get(id)
}
func (s *Store) List() ([]Application, error) {
	rows, err := s.db.Query(applicationSelect + ` WHERE a.archived_at IS NULL ORDER BY a.created_at DESC`)
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
	return scan(s.db.QueryRow(applicationSelect+` WHERE a.id=? AND a.archived_at IS NULL`, id))
}

const applicationSelect = `SELECT a.id,a.slug,a.name,a.description,a.status,COALESCE(m.name,''),a.created_at,s.source_type,a.source_path,COALESCE(s.connection_id,''),COALESCE(s.installation_id,0),COALESCE(s.repository_id,0),COALESCE(s.repository_owner,''),COALESCE(s.repository_name,''),COALESCE(s.tracked_branch,''),COALESCE(s.tracked_ref,''),COALESCE(s.compose_path,''),COALESCE(s.resolved_sha,'') FROM applications a LEFT JOIN machines m ON m.id=a.active_machine_id JOIN application_sources s ON s.application_id=a.id`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Application, error) {
	var a Application
	var created string
	err := row.Scan(&a.ID, &a.Slug, &a.Name, &a.Description, &a.Status, &a.MachineName, &created, &a.Source.Type, &a.Source.Path, &a.Source.ConnectionID, &a.Source.InstallationID, &a.Source.RepositoryID, &a.Source.RepositoryOwner, &a.Source.RepositoryName, &a.Source.TrackedBranch, &a.Source.TrackedRef, &a.Source.ComposePath, &a.Source.ResolvedSHA)
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return a, err
}

func validateSource(source *Source) error {
	source.Type = strings.TrimSpace(source.Type)
	if source.Type == "" {
		source.Type = SourceLocal
	}
	source.Path = strings.TrimSpace(source.Path)
	if source.Type == SourceLocal {
		if source.ConnectionID != "" || source.InstallationID != 0 || source.RepositoryID != 0 || source.RepositoryOwner != "" || source.RepositoryName != "" || source.TrackedBranch != "" || source.TrackedRef != "" || source.ComposePath != "" || source.ResolvedSHA != "" {
			return errors.New("local source contains GitHub metadata")
		}
		return nil
	}
	if source.Type != SourceGitHub || source.Path != "" || source.ConnectionID == "" || source.InstallationID < 1 || source.RepositoryID < 1 || source.RepositoryOwner == "" || source.RepositoryName == "" || source.TrackedBranch == "" || source.TrackedRef != "refs/heads/"+source.TrackedBranch || source.ComposePath == "" || len(source.ResolvedSHA) != 40 {
		return errors.New("invalid GitHub source")
	}
	return nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
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
