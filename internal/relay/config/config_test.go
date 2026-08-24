package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type memorySource struct {
	env   map[string]string
	files map[string]File
	err   map[string]error
}

func (s *memorySource) LookupEnv(name string) (string, bool) {
	value, ok := s.env[name]
	return value, ok
}
func (s *memorySource) ReadFile(path string, _ int64) (File, error) {
	if err := s.err[path]; err != nil {
		return File{}, err
	}
	f, ok := s.files[path]
	if !ok {
		return File{}, errors.New("not found: secret-value-should-not-leak")
	}
	return f, nil
}

func validSource(t *testing.T) *memorySource {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	env := map[string]string{
		EnvPublicBaseURL:   "https://relay.example.test",
		EnvPostgresDSNFile: "dsn", EnvGitHubClientID: "Iv1.test_client", EnvGitHubAppID: "42",
		EnvGitHubClientSecretFile: "client-secret", EnvGitHubPrivateKeyFile: "app-key",
		EnvWebhookSecretFile: "webhook-secret", EnvEnrollmentKeyFile: "enrollment-key",
	}
	files := map[string]File{
		"dsn":            {Data: []byte("postgres://relay:password@database/relay?sslmode=require"), Mode: 0o600, Regular: true},
		"client-secret":  {Data: []byte("0123456789abcdef-client"), Mode: 0o600, Regular: true},
		"app-key":        {Data: privatePEM, Mode: 0o600, Regular: true},
		"webhook-secret": {Data: []byte("0123456789abcdef-webhook"), Mode: 0o600, Regular: true},
		"enrollment-key": {Data: make([]byte, 32), Mode: 0o600, Regular: true},
	}
	for i := range files["enrollment-key"].Data {
		files["enrollment-key"].Data[i] = byte(i)
	}
	return &memorySource{env: env, files: files, err: map[string]error{}}
}

func TestLoadValidConfigurationAndClearsReaderBuffers(t *testing.T) {
	source := validSource(t)
	originalEnrollment := append([]byte(nil), source.files["enrollment-key"].Data...)
	configuration, err := Load(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(configuration.DestroySecrets)
	if configuration.PublicBaseURL.String() != "https://relay.example.test" || configuration.GitHubAppID != 42 {
		t.Fatalf("unexpected config: %v", configuration)
	}
	if string(configuration.EnrollmentKey) != string(originalEnrollment) {
		t.Fatal("binary enrollment key bytes changed")
	}
	for path, file := range source.files {
		for _, b := range file.Data {
			if b != 0 {
				t.Fatalf("reader-owned buffer %q was not cleared", path)
			}
		}
	}
	if GitHubWebOrigin != "https://github.com" || GitHubAPIOrigin != "https://api.github.com" {
		t.Fatal("GitHub origins are not fixed")
	}
}

func TestRequiredAndInvalidEnvironment(t *testing.T) {
	requiredNames := []string{EnvPublicBaseURL, EnvPostgresDSNFile, EnvGitHubClientID, EnvGitHubAppID, EnvGitHubClientSecretFile, EnvGitHubPrivateKeyFile, EnvWebhookSecretFile, EnvEnrollmentKeyFile}
	for _, name := range requiredNames {
		t.Run("missing "+name, func(t *testing.T) {
			source := validSource(t)
			delete(source.env, name)
			_, err := Load(source)
			assertConfigCode(t, err, CodeMissing)
		})
	}
	for _, test := range []struct{ name, field, value string }{
		{"empty", EnvGitHubClientID, ""}, {"surrounding whitespace", EnvGitHubClientID, " client"}, {"nul", EnvGitHubClientID, "client\x00id"},
		{"app id zero", EnvGitHubAppID, "0"}, {"app id negative", EnvGitHubAppID, "-1"}, {"app id text", EnvGitHubAppID, "forty-two"},
		{"bad boolean", EnvLoopbackDevelopment, "yes"}, {"bad listen hostname", EnvListenAddress, "localhost:7346"}, {"bad listen port", EnvListenAddress, "127.0.0.1:http"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := validSource(t)
			source.env[test.field] = test.value
			_, err := Load(source)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadPreservesSecretNewlines(t *testing.T) {
	source := validSource(t)
	file := source.files["webhook-secret"]
	file.Data = []byte("0123456789abcdef\n")
	source.files["webhook-secret"] = file
	configuration, err := Load(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(configuration.DestroySecrets)
	if got := string(configuration.WebhookSecret); got != "0123456789abcdef\n" {
		t.Fatalf("webhook secret = %q", got)
	}
}

func TestPublicURLAndListenValidation(t *testing.T) {
	tests := []struct {
		name, public, listen, development string
		ok                                bool
	}{
		{name: "production https", public: "https://relay.example.test", listen: "0.0.0.0:7346", ok: true},
		{name: "production http rejected", public: "http://relay.example.test", listen: "0.0.0.0:7346"},
		{name: "loopback development", public: "http://127.0.0.1:7346", listen: "127.0.0.1:7346", development: "true", ok: true},
		{name: "development public nonloopback", public: "http://192.0.2.1:7346", listen: "127.0.0.1:7346", development: "true"},
		{name: "development wildcard listen", public: "http://127.0.0.1:7346", listen: "0.0.0.0:7346", development: "true"},
		{name: "credentials", public: "https://user:pass@relay.example.test", listen: "0.0.0.0:7346"},
		{name: "path", public: "https://relay.example.test/path", listen: "0.0.0.0:7346"},
		{name: "query", public: "https://relay.example.test?x=1", listen: "0.0.0.0:7346"},
		{name: "fragment", public: "https://relay.example.test#x", listen: "0.0.0.0:7346"},
		{name: "localhost is not explicit IP", public: "http://localhost:7346", listen: "127.0.0.1:7346", development: "true"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := validSource(t)
			source.env[EnvPublicBaseURL] = test.public
			source.env[EnvListenAddress] = test.listen
			if test.development != "" {
				source.env[EnvLoopbackDevelopment] = test.development
			}
			configuration, err := Load(source)
			if (err == nil) != test.ok {
				t.Fatalf("Load() error = %v, want success %v", err, test.ok)
			}
			configuration.DestroySecrets()
		})
	}
}

func TestPostgresDSNValidationDoesNotEchoInput(t *testing.T) {
	tests := []struct {
		name  string
		value []byte
	}{
		{"empty", nil}, {"nul", []byte("postgres://user:password@host/db\x00evil")}, {"newline", []byte("postgres://user:password@host/db\nLOG_INJECTION")},
		{"http scheme", []byte("https://user:password@host/db")}, {"missing credentials", []byte("postgres://host/db")}, {"oversized", make([]byte, (16<<10)+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := validSource(t)
			file := source.files["dsn"]
			file.Data = test.value
			source.files["dsn"] = file
			_, err := Load(source)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "LOG_INJECTION") {
				t.Fatalf("DSN leaked: %v", err)
			}
		})
	}
}

func TestProtectedFileValidationAndBounds(t *testing.T) {
	t.Run("oversize", func(t *testing.T) {
		source := validSource(t)
		file := source.files["webhook-secret"]
		file.Data = make([]byte, (64<<10)+1)
		source.files["webhook-secret"] = file
		_, err := Load(source)
		assertConfigCode(t, err, CodeFileTooLarge)
	})
	t.Run("non regular", func(t *testing.T) {
		source := validSource(t)
		file := source.files["webhook-secret"]
		file.Regular = false
		source.files["webhook-secret"] = file
		_, err := Load(source)
		assertConfigCode(t, err, CodeFileMode)
	})
	if runtime.GOOS != "windows" {
		t.Run("broad permissions", func(t *testing.T) {
			source := validSource(t)
			file := source.files["webhook-secret"]
			file.Mode = 0o644
			source.files["webhook-secret"] = file
			_, err := Load(source)
			assertConfigCode(t, err, CodeFileMode)
		})
	}
	t.Run("enrollment exact bytes", func(t *testing.T) {
		source := validSource(t)
		file := source.files["enrollment-key"]
		file.Data = make([]byte, 31)
		source.files["enrollment-key"] = file
		_, err := Load(source)
		assertConfigCode(t, err, CodeInvalid)
	})
}

func TestTLSCertificatePairValidation(t *testing.T) {
	cert, key := testCertificatePair(t)
	t.Run("valid", func(t *testing.T) {
		source := validSource(t)
		source.env[EnvTLSCertificateFile], source.env[EnvTLSPrivateKeyFile] = "cert", "tls-key"
		source.files["cert"] = File{Data: cert, Mode: 0o644, Regular: true}
		source.files["tls-key"] = File{Data: key, Mode: 0o600, Regular: true}
		configuration, err := Load(source)
		if err != nil {
			t.Fatal(err)
		}
		configuration.DestroySecrets()
	})
	t.Run("mismatch", func(t *testing.T) {
		_, otherKey := testCertificatePair(t)
		source := validSource(t)
		source.env[EnvTLSCertificateFile], source.env[EnvTLSPrivateKeyFile] = "cert", "tls-key"
		source.files["cert"] = File{Data: cert, Mode: 0o644, Regular: true}
		source.files["tls-key"] = File{Data: otherKey, Mode: 0o600, Regular: true}
		_, err := Load(source)
		assertConfigCode(t, err, CodeInvalid)
		for _, path := range []string{"cert", "tls-key"} {
			for _, b := range source.files[path].Data {
				if b != 0 {
					t.Fatalf("%s input buffer not cleared", path)
				}
			}
		}
	})
	t.Run("pair required", func(t *testing.T) {
		source := validSource(t)
		source.env[EnvTLSCertificateFile] = "cert"
		source.files["cert"] = File{Data: cert, Mode: 0o644, Regular: true}
		_, err := Load(source)
		assertConfigCode(t, err, CodeInvalid)
	})
}

func TestRecoveryWindowIsCappedAtGitHubRedeliveryHorizon(t *testing.T) {
	source := validSource(t)
	source.env[EnvRecoveryWindow] = "72h1s"
	_, err := Load(source)
	assertConfigCode(t, err, CodeInvalid)
}

func TestDurationAndLimitBounds(t *testing.T) {
	tests := []struct{ name, env, value string }{
		{"read too short", EnvReadTimeout, "999ms"}, {"write too long", EnvWriteTimeout, "61s"}, {"idle too short", EnvIdleTimeout, "9s"},
		{"recovery interval too short", EnvRecoveryInterval, "999ms"}, {"recovery order", EnvRecoveryInterval, "24h"},
		{"minimum session too short", EnvMinSessionDuration, "59s"}, {"maximum session too long", EnvMaxSessionDuration, "721h"},
		{"envelope too small", EnvMaxEnvelopeBytes, "4095"}, {"envelope too large", EnvMaxEnvelopeBytes, "8388609"},
		{"subscriptions zero", EnvMaxSubscriptions, "0"}, {"subscriptions above protocol cap", EnvMaxSubscriptions, "1001"},
		{"sources zero", EnvMaxSourcesPerSubscription, "0"}, {"sources huge", EnvMaxSourcesPerSubscription, "100001"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := validSource(t)
			source.env[test.env] = test.value
			_, err := Load(source)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRSAPrivateKeyValidation(t *testing.T) {
	tests := []struct {
		name string
		key  func(*testing.T) []byte
	}{
		{"malformed", func(*testing.T) []byte { return []byte("not a key") }},
		{"too small", func(t *testing.T) []byte {
			key, err := rsa.GenerateKey(rand.Reader, 1024)
			if err != nil {
				t.Fatal(err)
			}
			return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		}},
		{"non RSA PKCS8", func(t *testing.T) []byte {
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		}},
		{"wrong pem type", func(*testing.T) []byte {
			return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("data")})
		}},
		{"wrong PKCS1 bytes", func(*testing.T) []byte {
			return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not-der")})
		}},
		{"multiple blocks", func(t *testing.T) []byte {
			source := validSource(t)
			return append(append([]byte(nil), source.files["app-key"].Data...), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("second")})...)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := validSource(t)
			file := source.files["app-key"]
			file.Data = test.key(t)
			source.files["app-key"] = file
			_, err := Load(source)
			assertConfigCode(t, err, CodeInvalid)
		})
	}
}

func TestPKCS8RSAPrivateKeyIsAccepted(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	source := validSource(t)
	file := source.files["app-key"]
	file.Data = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	source.files["app-key"] = file
	configuration, err := Load(source)
	if err != nil {
		t.Fatal(err)
	}
	configuration.DestroySecrets()
}

func TestOSSourceRejectsNonRegularAndSymlink(t *testing.T) {
	source := OSSource{}
	directory := t.TempDir()
	if _, err := source.ReadFile(directory, 100); err == nil {
		t.Fatal("directory accepted")
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := source.ReadFile(link, 100); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestProtectedModePolicyIsExplicitPerPlatform(t *testing.T) {
	source := validSource(t)
	file := source.files["webhook-secret"]
	file.Mode = 0o644
	source.files["webhook-secret"] = file
	configuration, err := Load(source)
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("Windows cannot portably enforce POSIX mode bits: %v", err)
		}
		configuration.DestroySecrets()
		return
	}
	assertConfigCode(t, err, CodeFileMode)
}

func TestDestroySecretsAndSerializationSafety(t *testing.T) {
	configuration := Config{PostgresDSN: Secret("postgres-secret"), GitHubClientSecret: Secret("client-secret-1234"), GitHubPrivateKey: Secret("private-key"), WebhookSecret: Secret("webhook-secret-1234"), EnrollmentKey: Secret("01234567890123456789012345678901"), TLSCertificate: []byte("certificate"), TLSPrivateKey: Secret("tls-private-key")}
	buffers := [][]byte{configuration.PostgresDSN, configuration.GitHubClientSecret, configuration.GitHubPrivateKey, configuration.WebhookSecret, configuration.EnrollmentKey, configuration.TLSCertificate, configuration.TLSPrivateKey}
	secretText := "serialization-secret"
	if output, err := json.Marshal(Secret(secretText)); err == nil || len(output) != 0 || strings.Contains(err.Error(), secretText) {
		t.Fatalf("Secret serialized or leaked: %q, %v", output, err)
	}
	if output, err := Secret(secretText).MarshalText(); err == nil || len(output) != 0 || strings.Contains(err.Error(), secretText) {
		t.Fatalf("Secret text serialized or leaked: %q, %v", output, err)
	}
	if output, err := json.Marshal(configuration); err == nil || len(output) != 0 || strings.Contains(err.Error(), "postgres-secret") {
		t.Fatalf("Config serialized or leaked: %q, %v", output, err)
	}
	configuration.DestroySecrets()
	for i, buffer := range buffers {
		for _, b := range buffer {
			if b != 0 {
				t.Fatalf("buffer %d not cleared", i)
			}
		}
	}
}

func TestErrorsAndConfigRepresentationsRedactSecrets(t *testing.T) {
	source := validSource(t)
	secret := "top-secret-do-not-log"
	source.env[EnvPostgresDSNFile] = secret
	source.err[secret] = fmt.Errorf("read %s failed", secret)
	_, err := Load(source)
	if err == nil {
		t.Fatal("expected error")
	}
	var configErr *Error
	if !errors.As(err, &configErr) {
		t.Fatalf("error type = %T", err)
	}
	for _, representation := range []string{err.Error(), configErr.String(), configErr.LogValue().String()} {
		if strings.Contains(representation, secret) {
			t.Fatalf("secret leaked: %q", representation)
		}
	}
	configuration := Defaults()
	configuration.PostgresDSN = Secret(secret)
	configuration.WebhookSecret = Secret(secret)
	for _, representation := range []string{configuration.String(), fmt.Sprintf("%v", configuration), fmt.Sprintf("%+v", configuration), fmt.Sprintf("%#v", configuration), configuration.LogValue().String(), slog.AnyValue(configuration).String()} {
		if strings.Contains(representation, secret) {
			t.Fatalf("config leaked: %q", representation)
		}
	}
	secretValue := Secret(secret)
	if got := secretValue.String(); got != "[REDACTED]" {
		t.Fatalf("String = %q", got)
	}
	if got := secretValue.GoString(); got != "config.Secret([REDACTED])" {
		t.Fatalf("GoString = %q", got)
	}
	for _, representation := range []string{fmt.Sprintf("%s", secretValue), fmt.Sprintf("%v", secretValue), fmt.Sprintf("%+v", secretValue), fmt.Sprintf("%#v", secretValue), secretValue.LogValue().String(), slog.AnyValue(secretValue).String()} {
		if strings.Contains(representation, secret) || !strings.Contains(representation, "REDACTED") {
			t.Fatalf("unsafe secret representation: %q", representation)
		}
	}
}

func testCertificatePair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "relay.example.test"}, DNSNames: []string{"relay.example.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func assertConfigCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var target *Error
	if !errors.As(err, &target) || target.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}
