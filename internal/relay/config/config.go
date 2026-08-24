// Package config loads and validates the configuration for the independently
// deployable webhook relay. Secret material is accepted only through protected
// files; this package intentionally has no command-line parser.
package config

import (
	"bytes"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	GitHubWebOrigin = "https://github.com"
	GitHubAPIOrigin = "https://api.github.com"

	EnvListenAddress             = "HOSTD_RELAY_LISTEN_ADDRESS"
	EnvPublicBaseURL             = "HOSTD_RELAY_PUBLIC_BASE_URL"
	EnvLoopbackDevelopment       = "HOSTD_RELAY_LOOPBACK_DEVELOPMENT"
	EnvPostgresDSNFile           = "HOSTD_RELAY_POSTGRES_DSN_FILE"
	EnvGitHubClientID            = "HOSTD_RELAY_GITHUB_CLIENT_ID"
	EnvGitHubAppID               = "HOSTD_RELAY_GITHUB_APP_ID"
	EnvGitHubClientSecretFile    = "HOSTD_RELAY_GITHUB_CLIENT_SECRET_FILE"
	EnvGitHubPrivateKeyFile      = "HOSTD_RELAY_GITHUB_PRIVATE_KEY_FILE"
	EnvWebhookSecretFile         = "HOSTD_RELAY_WEBHOOK_SECRET_FILE"
	EnvEnrollmentKeyFile         = "HOSTD_RELAY_ENROLLMENT_KEY_FILE"
	EnvTLSCertificateFile        = "HOSTD_RELAY_TLS_CERTIFICATE_FILE"
	EnvTLSPrivateKeyFile         = "HOSTD_RELAY_TLS_PRIVATE_KEY_FILE"
	EnvReadTimeout               = "HOSTD_RELAY_READ_TIMEOUT"
	EnvWriteTimeout              = "HOSTD_RELAY_WRITE_TIMEOUT"
	EnvIdleTimeout               = "HOSTD_RELAY_IDLE_TIMEOUT"
	EnvRecoveryInterval          = "HOSTD_RELAY_RECOVERY_INTERVAL"
	EnvRecoveryWindow            = "HOSTD_RELAY_RECOVERY_WINDOW"
	EnvMinSessionDuration        = "HOSTD_RELAY_MIN_SESSION_DURATION"
	EnvMaxSessionDuration        = "HOSTD_RELAY_MAX_SESSION_DURATION"
	EnvMaxEnvelopeBytes          = "HOSTD_RELAY_MAX_ENVELOPE_BYTES"
	EnvMaxSubscriptions          = "HOSTD_RELAY_MAX_SUBSCRIPTIONS"
	EnvMaxSourcesPerSubscription = "HOSTD_RELAY_MAX_SOURCES_PER_SUBSCRIPTION"

	maxSecretFileBytes = 1 << 20
	maxTLSFileBytes    = 4 << 20
)

type ErrorCode string

const (
	CodeMissing      ErrorCode = "relay_config_missing"
	CodeInvalid      ErrorCode = "relay_config_invalid"
	CodeFile         ErrorCode = "relay_config_file"
	CodeFileMode     ErrorCode = "relay_config_file_mode"
	CodeFileTooLarge ErrorCode = "relay_config_file_too_large"
)

// Error deliberately excludes environment values, paths, and wrapped I/O
// errors from its public representation so it is safe for logs and responses.
type Error struct {
	Code  ErrorCode
	Field string
	cause error
}

func (e *Error) Error() string {
	if e == nil {
		return "relay configuration error"
	}
	return fmt.Sprintf("relay configuration error: code=%s field=%s", e.Code, e.Field)
}

func (e *Error) Unwrap() error  { return e.cause }
func (e *Error) String() string { return e.Error() }
func (e *Error) LogValue() slog.Value {
	return slog.GroupValue(slog.String("code", string(e.Code)), slog.String("field", e.Field))
}

type Secret []byte

func (Secret) String() string   { return "[REDACTED]" }
func (Secret) GoString() string { return "config.Secret([REDACTED])" }
func (Secret) LogValue() slog.Value {
	return slog.StringValue("[REDACTED]")
}
func (Secret) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, "[REDACTED]") }
func (Secret) MarshalJSON() ([]byte, error) {
	return nil, errors.New("relay configuration secrets cannot be serialized")
}
func (Secret) MarshalText() ([]byte, error) {
	return nil, errors.New("relay configuration secrets cannot be serialized")
}

func (s Secret) Clone() []byte { return append([]byte(nil), s...) }
func (s Secret) Destroy()      { clear(s) }

type Config struct {
	ListenAddress       string
	PublicBaseURL       *url.URL
	LoopbackDevelopment bool

	PostgresDSN        Secret
	GitHubClientID     string
	GitHubAppID        int64
	GitHubClientSecret Secret
	GitHubPrivateKey   Secret
	WebhookSecret      Secret
	EnrollmentKey      Secret

	TLSCertificate []byte
	TLSPrivateKey  Secret

	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	RecoveryInterval   time.Duration
	RecoveryWindow     time.Duration
	MinSessionDuration time.Duration
	MaxSessionDuration time.Duration
	MaxEnvelopeBytes   int
	MaxSubscriptions   int
	MaxSources         int
}

func (c Config) String() string {
	return fmt.Sprintf("relay config (listen_mode=%s public_url_mode=%s github_client_configured=%t github_app_configured=%t tls_mode=%s)",
		listenMode(c.ListenAddress), publicURLMode(c.PublicBaseURL), c.GitHubClientID != "", c.GitHubAppID > 0, tlsMode(c.TLSCertificate))
}

func (c Config) GoString() string               { return c.String() }
func (c Config) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, c.String()) }
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("listen_mode", listenMode(c.ListenAddress)),
		slog.String("public_url_mode", publicURLMode(c.PublicBaseURL)),
		slog.Bool("loopback_development", c.LoopbackDevelopment),
		slog.Bool("github_client_configured", c.GitHubClientID != ""),
		slog.Bool("github_app_configured", c.GitHubAppID > 0),
		slog.String("tls_mode", tlsMode(c.TLSCertificate)),
		slog.Duration("read_timeout", c.ReadTimeout),
		slog.Duration("write_timeout", c.WriteTimeout),
		slog.Duration("idle_timeout", c.IdleTimeout),
		slog.Duration("recovery_interval", c.RecoveryInterval),
		slog.Duration("recovery_window", c.RecoveryWindow),
		slog.Duration("min_session_duration", c.MinSessionDuration),
		slog.Duration("max_session_duration", c.MaxSessionDuration),
		slog.Int("max_envelope_bytes", c.MaxEnvelopeBytes),
		slog.Int("max_subscriptions", c.MaxSubscriptions),
		slog.Int("max_sources", c.MaxSources),
	)
}

func listenMode(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		if address == "" {
			return "unset"
		}
		return "invalid"
	}
	if loopbackHost(host) {
		return "loopback"
	}
	return "network"
}

func publicURLMode(value *url.URL) string {
	if value == nil {
		return "unset"
	}
	if value.Scheme == "https" {
		return "https"
	}
	if value.Scheme == "http" && loopbackHost(value.Hostname()) {
		return "loopback_http"
	}
	return "invalid"
}

func tlsMode(certificate []byte) string {
	if len(certificate) == 0 {
		return "disabled"
	}
	return "enabled"
}

func (c *Config) DestroySecrets() {
	if c == nil {
		return
	}
	c.PostgresDSN.Destroy()
	c.GitHubClientSecret.Destroy()
	c.GitHubPrivateKey.Destroy()
	c.WebhookSecret.Destroy()
	c.EnrollmentKey.Destroy()
	c.TLSPrivateKey.Destroy()
	clear(c.TLSCertificate)
}

type File struct {
	Data    []byte
	Mode    fs.FileMode
	Regular bool
}

// Source provides both environment and protected-file access as injectable
// seams. Implementations must not include file contents in returned errors.
type Source interface {
	LookupEnv(name string) (string, bool)
	ReadFile(path string, maxBytes int64) (File, error)
}

type OSSource struct{}

func (OSSource) LookupEnv(name string) (string, bool) { return os.LookupEnv(name) }

func (OSSource) ReadFile(path string, maxBytes int64) (File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return File{}, err
	}
	if err := verifyPlatformFile(path); err != nil {
		return File{Mode: info.Mode()}, err
	}
	if !info.Mode().IsRegular() {
		return File{Mode: info.Mode()}, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return File{}, err
	}
	return File{Data: b, Mode: info.Mode(), Regular: true}, nil
}

func Defaults() Config {
	return Config{
		ListenAddress:      "127.0.0.1:7346",
		ReadTimeout:        15 * time.Second,
		WriteTimeout:       15 * time.Second,
		IdleTimeout:        75 * time.Second,
		RecoveryInterval:   30 * time.Second,
		RecoveryWindow:     24 * time.Hour,
		MinSessionDuration: 5 * time.Minute,
		MaxSessionDuration: 24 * time.Hour,
		MaxEnvelopeBytes:   1 << 20,
		MaxSubscriptions:   1000,
		MaxSources:         1000,
	}
}

func Load(source Source) (Config, error) {
	if source == nil {
		return Config{}, configError(CodeInvalid, "source", nil)
	}
	c := Defaults()
	var err error
	if c.ListenAddress, err = optional(source, EnvListenAddress, c.ListenAddress); err != nil {
		return Config{}, err
	}
	publicURL, err := required(source, EnvPublicBaseURL)
	if err != nil {
		return Config{}, err
	}
	c.LoopbackDevelopment, err = optionalBool(source, EnvLoopbackDevelopment, false)
	if err != nil {
		return Config{}, err
	}
	c.PublicBaseURL, err = validatePublicURL(publicURL, c.LoopbackDevelopment)
	if err != nil {
		return Config{}, err
	}
	if err = validateListenAddress(c.ListenAddress, c.LoopbackDevelopment); err != nil {
		return Config{}, err
	}
	if c.GitHubClientID, err = required(source, EnvGitHubClientID); err != nil {
		return Config{}, err
	}
	appID, err := required(source, EnvGitHubAppID)
	if err != nil {
		return Config{}, err
	}
	c.GitHubAppID, err = strconv.ParseInt(appID, 10, 64)
	if err != nil || c.GitHubAppID <= 0 {
		return Config{}, configError(CodeInvalid, EnvGitHubAppID, nil)
	}
	if !validIdentifier(c.GitHubClientID, 255) {
		return Config{}, configError(CodeInvalid, EnvGitHubClientID, nil)
	}

	secretSpecs := []struct {
		env   string
		dst   *Secret
		limit int64
	}{
		{EnvPostgresDSNFile, &c.PostgresDSN, 16 << 10},
		{EnvGitHubClientSecretFile, &c.GitHubClientSecret, 4 << 10},
		{EnvGitHubPrivateKeyFile, &c.GitHubPrivateKey, maxSecretFileBytes},
		{EnvWebhookSecretFile, &c.WebhookSecret, 64 << 10},
		{EnvEnrollmentKeyFile, &c.EnrollmentKey, 32},
	}
	for _, spec := range secretSpecs {
		*spec.dst, err = readProtected(source, spec.env, spec.limit)
		if err != nil {
			c.DestroySecrets()
			return Config{}, err
		}
	}
	if !validPostgresDSN(c.PostgresDSN) {
		return destroyAndError(&c, EnvPostgresDSNFile)
	}
	if len(c.GitHubClientSecret) < 16 || len(c.GitHubClientSecret) > 4<<10 || bytes.IndexByte(c.GitHubClientSecret, 0) >= 0 {
		return destroyAndError(&c, EnvGitHubClientSecretFile)
	}
	if _, err := parseRSAPrivateKey(c.GitHubPrivateKey); err != nil {
		return destroyAndError(&c, EnvGitHubPrivateKeyFile)
	}
	if len(c.WebhookSecret) < 16 || len(c.WebhookSecret) > 64<<10 || bytes.IndexByte(c.WebhookSecret, 0) >= 0 {
		return destroyAndError(&c, EnvWebhookSecretFile)
	}
	if len(c.EnrollmentKey) != 32 {
		return destroyAndError(&c, EnvEnrollmentKeyFile)
	}

	certPath, certSet := source.LookupEnv(EnvTLSCertificateFile)
	keyPath, keySet := source.LookupEnv(EnvTLSPrivateKeyFile)
	if certSet != keySet || (certSet && (certPath == "" || keyPath == "")) {
		return destroyAndError(&c, EnvTLSCertificateFile)
	}
	if certSet {
		cert, readErr := readFile(source, EnvTLSCertificateFile, certPath, maxTLSFileBytes, false)
		if readErr != nil {
			c.DestroySecrets()
			return Config{}, readErr
		}
		key, readErr := readFile(source, EnvTLSPrivateKeyFile, keyPath, maxTLSFileBytes, true)
		if readErr != nil {
			clear(cert)
			c.DestroySecrets()
			return Config{}, readErr
		}
		c.TLSCertificate, c.TLSPrivateKey = cert, Secret(key)
		if _, pairErr := tls.X509KeyPair(c.TLSCertificate, c.TLSPrivateKey); pairErr != nil {
			return destroyAndError(&c, EnvTLSPrivateKeyFile)
		}
	}

	durations := []struct {
		env     string
		dst     *time.Duration
		minimum time.Duration
		maximum time.Duration
	}{
		{EnvReadTimeout, &c.ReadTimeout, time.Second, time.Minute},
		{EnvWriteTimeout, &c.WriteTimeout, time.Second, time.Minute},
		{EnvIdleTimeout, &c.IdleTimeout, 10 * time.Second, 10 * time.Minute},
		{EnvRecoveryInterval, &c.RecoveryInterval, time.Second, time.Hour},
		{EnvRecoveryWindow, &c.RecoveryWindow, time.Minute, 72 * time.Hour},
		{EnvMinSessionDuration, &c.MinSessionDuration, time.Minute, 24 * time.Hour},
		{EnvMaxSessionDuration, &c.MaxSessionDuration, time.Minute, 30 * 24 * time.Hour},
	}
	for _, item := range durations {
		*item.dst, err = optionalDuration(source, item.env, *item.dst, item.minimum, item.maximum)
		if err != nil {
			c.DestroySecrets()
			return Config{}, err
		}
	}
	if c.RecoveryInterval >= c.RecoveryWindow || c.MinSessionDuration > c.MaxSessionDuration || c.IdleTimeout >= c.MaxSessionDuration {
		return destroyAndError(&c, "duration_bounds")
	}
	c.MaxEnvelopeBytes, err = optionalInt(source, EnvMaxEnvelopeBytes, c.MaxEnvelopeBytes, 4<<10, 8<<20)
	if err == nil {
		c.MaxSubscriptions, err = optionalInt(source, EnvMaxSubscriptions, c.MaxSubscriptions, 1, 1000)
	}
	if err == nil {
		c.MaxSources, err = optionalInt(source, EnvMaxSourcesPerSubscription, c.MaxSources, 1, 100_000)
	}
	if err != nil {
		c.DestroySecrets()
		return Config{}, err
	}
	return c, nil
}

func LoadOS() (Config, error) { return Load(OSSource{}) }

func required(source Source, name string) (string, error) {
	v, ok := source.LookupEnv(name)
	if !ok || v == "" {
		return "", configError(CodeMissing, name, nil)
	}
	if strings.TrimSpace(v) != v || strings.IndexByte(v, 0) >= 0 {
		return "", configError(CodeInvalid, name, nil)
	}
	return v, nil
}

func optional(source Source, name, fallback string) (string, error) {
	v, ok := source.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	if v == "" || strings.TrimSpace(v) != v || strings.IndexByte(v, 0) >= 0 {
		return "", configError(CodeInvalid, name, nil)
	}
	return v, nil
}

func optionalBool(source Source, name string, fallback bool) (bool, error) {
	v, ok := source.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, configError(CodeInvalid, name, nil)
	}
	return b, nil
}

func optionalDuration(source Source, name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	v, ok := source.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < minimum || d > maximum {
		return 0, configError(CodeInvalid, name, nil)
	}
	return d, nil
}

func optionalInt(source Source, name string, fallback, minimum, maximum int) (int, error) {
	v, ok := source.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minimum || n > maximum {
		return 0, configError(CodeInvalid, name, nil)
	}
	return n, nil
}

func readProtected(source Source, env string, limit int64) (Secret, error) {
	path, err := required(source, env)
	if err != nil {
		return nil, err
	}
	b, err := readFile(source, env, path, limit, true)
	return Secret(b), err
}

// readFile copies bytes exactly, including trailing newlines in HMAC/PEM and
// arbitrary bytes in enrollment keys. It never performs text normalization.
func readFile(source Source, field, path string, limit int64, protected bool) ([]byte, error) {
	f, err := source.ReadFile(path, limit)
	if err != nil {
		return nil, configError(CodeFile, field, err)
	}
	if !f.Regular {
		clear(f.Data)
		return nil, configError(CodeFileMode, field, nil)
	}
	if int64(len(f.Data)) > limit {
		clear(f.Data)
		return nil, configError(CodeFileTooLarge, field, nil)
	}
	if protected && runtime.GOOS != "windows" && f.Mode.Perm()&0o077 != 0 {
		clear(f.Data)
		return nil, configError(CodeFileMode, field, nil)
	}
	if len(f.Data) == 0 {
		return nil, configError(CodeInvalid, field, nil)
	}
	copyOfData := append([]byte(nil), f.Data...)
	clear(f.Data)
	return copyOfData, nil
}

func validatePublicURL(raw string, development bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, configError(CodeInvalid, EnvPublicBaseURL, nil)
	}
	if u.Scheme == "https" {
		return u, nil
	}
	if u.Scheme != "http" || !development || !loopbackHost(u.Hostname()) {
		return nil, configError(CodeInvalid, EnvPublicBaseURL, nil)
	}
	return u, nil
}

func validateListenAddress(address string, development bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return configError(CodeInvalid, EnvListenAddress, nil)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 || net.ParseIP(host) == nil {
		return configError(CodeInvalid, EnvListenAddress, nil)
	}
	if development && !loopbackHost(host) {
		return configError(CodeInvalid, EnvListenAddress, nil)
	}
	return nil
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validIdentifier(v string, maximum int) bool {
	if len(v) == 0 || len(v) > maximum {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._-", r) {
			return false
		}
	}
	return true
}

func validPostgresDSN(value []byte) bool {
	if len(value) == 0 || len(value) > 16<<10 || bytes.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, b := range value {
		if b == '\r' || b == '\n' || b == '\t' || b == ' ' {
			return false
		}
	}
	u, err := url.Parse(string(value))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || u.Fragment != "" || u.User == nil {
		return false
	}
	return true
}

func parseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid PEM")
	}
	var key any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, errors.New("unsupported private key")
	}
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok || rsaKey.N.BitLen() < 2048 || rsaKey.Validate() != nil {
		return nil, errors.New("invalid RSA private key")
	}
	return rsaKey, nil
}

func configError(code ErrorCode, field string, cause error) error {
	return &Error{Code: code, Field: field, cause: cause}
}

func destroyAndError(c *Config, field string) (Config, error) {
	c.DestroySecrets()
	return Config{}, configError(CodeInvalid, field, nil)
}
