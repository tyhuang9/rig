package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

const (
	SessionCookie          = "hostd_session"
	BootstrapSecretPurpose = "bootstrap-token"
	BootstrapTokenLifetime = 15 * time.Minute
)

type Service struct {
	db             *sql.DB
	now            func() time.Time
	hashPassphrase func(string) (string, error)
}
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}
type Session struct {
	Token, CSRF string
	ExpiresAt   time.Time
}

func New(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now, hashPassphrase: hashPassphrase}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

func hashPassphrase(passphrase string) (string, error) {
	if len(passphrase) < 12 {
		return "", errors.New("passphrase must be at least 12 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func verifyPassphrase(encoded, passphrase string) bool {
	var memory uint32
	var iterations uint32
	var parallelism uint8
	var salt, expected []byte
	parts := split(encoded, '$')
	if len(parts) != 5 {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	var err error
	salt, err = base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	expected, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(passphrase), salt, iterations, memory, parallelism, uint32(len(expected)))
	if len(actual) != len(expected) {
		return false
	}
	var diff byte
	for i := range actual {
		diff |= actual[i] ^ expected[i]
	}
	return diff == 0
}
func split(s string, sep byte) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func (s *Service) BootstrapStatus() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n == 0, err
}
func (s *Service) EnsureBootstrapToken() (string, error) {
	needed, err := s.BootstrapStatus()
	if err != nil || !needed {
		return "", err
	}
	// Rotate on every pre-bootstrap daemon start so the protected token file
	// always has recoverable material matching the database hash.
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO bootstrap_tokens(id,token_hash,expires_at) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at,used_at=NULL`, digest(token), s.now().Add(BootstrapTokenLifetime).UTC().Format(time.RFC3339Nano))
	return token, err
}
func (s *Service) Bootstrap(token, username, passphrase string) (User, Session, error) {
	if username == "" {
		return User{}, Session{}, errors.New("username is required")
	}
	if err := s.validateBootstrapToken(s.db, token); err != nil {
		return User{}, Session{}, err
	}
	hash, err := s.hashPassphrase(passphrase)
	if err != nil {
		return User{}, Session{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, Session{}, err
	}
	defer tx.Rollback()
	if err = s.validateBootstrapToken(tx, token); err != nil {
		return User{}, Session{}, err
	}
	u := User{ID: uuid.NewString(), Username: username, Role: "administrator"}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec(`INSERT INTO users(id,username,passphrase_hash,role,created_at,updated_at) VALUES(?,?,?,?,?,?)`, u.ID, u.Username, hash, u.Role, now, now); err != nil {
		return User{}, Session{}, err
	}
	if _, err = tx.Exec(`UPDATE bootstrap_tokens SET used_at=? WHERE id=1`, now); err != nil {
		return User{}, Session{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, Session{}, err
	}
	session, err := s.NewSession(u.ID)
	return u, session, err
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func (s *Service) validateBootstrapToken(queryer rowQuerier, token string) error {
	var users int
	if err := queryer.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return err
	}
	if users != 0 {
		return errors.New("bootstrap is no longer available")
	}
	var tokenHash string
	var expires string
	if err := queryer.QueryRow(`SELECT token_hash, expires_at FROM bootstrap_tokens WHERE id=1 AND used_at IS NULL`).Scan(&tokenHash, &expires); err != nil {
		return errors.New("invalid bootstrap token")
	}
	suppliedHash := digest(token)
	if subtle.ConstantTimeCompare([]byte(tokenHash), []byte(suppliedHash)) != 1 || expires < s.now().UTC().Format(time.RFC3339Nano) {
		return errors.New("invalid bootstrap token")
	}
	return nil
}
func (s *Service) Login(username, passphrase string) (User, Session, error) {
	var u User
	var hash string
	err := s.db.QueryRow(`SELECT id,username,role,passphrase_hash FROM users WHERE username=?`, username).Scan(&u.ID, &u.Username, &u.Role, &hash)
	if err != nil || !verifyPassphrase(hash, passphrase) {
		return User{}, Session{}, errors.New("invalid credentials")
	}
	session, err := s.NewSession(u.ID)
	return u, session, err
}
func (s *Service) NewSession(userID string) (Session, error) {
	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	expires := s.now().Add(24 * time.Hour)
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO sessions(id,user_id,token_hash,csrf_hash,expires_at,created_at,last_seen_at) VALUES(?,?,?,?,?,?,?)`, uuid.NewString(), userID, digest(token), digest(csrf), expires.UTC().Format(time.RFC3339Nano), now, now)
	return Session{Token: token, CSRF: csrf, ExpiresAt: expires}, err
}
func (s *Service) Authenticate(token string) (User, string, error) {
	var u User
	var csrfHash, expiry string
	err := s.db.QueryRow(`SELECT u.id,u.username,u.role,s.csrf_hash,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.revoked_at IS NULL`, digest(token)).Scan(&u.ID, &u.Username, &u.Role, &csrfHash, &expiry)
	if err != nil || expiry < s.now().UTC().Format(time.RFC3339Nano) {
		return User{}, "", errors.New("unauthenticated")
	}
	_, _ = s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE token_hash=?`, s.now().UTC().Format(time.RFC3339Nano), digest(token))
	return u, csrfHash, nil
}
func (s *Service) CheckCSRF(expectedHash, supplied string) bool {
	return supplied != "" && digest(supplied) == expectedHash
}
func (s *Service) RotateCSRF(sessionToken string) (string, error) {
	csrf, err := randomToken()
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(`UPDATE sessions SET csrf_hash=?,last_seen_at=? WHERE token_hash=? AND revoked_at IS NULL AND expires_at>?`, digest(csrf), now, digest(sessionToken), now)
	if err != nil {
		return "", err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if updated != 1 {
		return "", errors.New("unauthenticated")
	}
	return csrf, nil
}
func (s *Service) Logout(token string) error {
	_, err := s.db.Exec(`UPDATE sessions SET revoked_at=? WHERE token_hash=?`, s.now().UTC().Format(time.RFC3339Nano), digest(token))
	return err
}
