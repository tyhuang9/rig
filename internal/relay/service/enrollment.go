package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/relay/protocol"
	"github.com/hostd/hostd/internal/relay/store"
)

const (
	startEnrollmentPath    = "/v1/enrollments"
	oauthCallbackPath      = "/v1/github/callback"
	pollEnrollmentPath     = "/v1/enrollments/status"
	maxEnrollmentBody      = 64 << 10
	enrollmentLifetime     = 10 * time.Minute
	enrollmentClockSkew    = time.Minute
	enrollmentStoreTimeout = 5 * time.Second
)

type startEnrollmentRequest struct {
	ControllerID   string    `json:"controllerId"`
	KeyID          string    `json:"keyId"`
	PublicKey      string    `json:"publicKey"`
	InstallationID int64     `json:"installationId"`
	RepositoryID   int64     `json:"repositoryId"`
	RequestNonce   string    `json:"requestNonce"`
	IssuedAt       time.Time `json:"issuedAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
	Signature      string    `json:"signature"`
}

type pollEnrollmentRequest struct {
	PollToken string `json:"pollToken"`
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(startEnrollmentPath, s.handleStartEnrollment)
	mux.HandleFunc(oauthCallbackPath, s.handleOAuthCallback)
	mux.HandleFunc(pollEnrollmentPath, s.handlePollEnrollment)
	mux.HandleFunc("/v1/github/webhook", s.handleWebhook)
	return mux
}

func (s *Service) handleStartEnrollment(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeProblem(w, http.StatusMethodNotAllowed, "request.method")
		return
	}
	// Rate limiting intentionally derives identity only from the peer address.
	// Forwarded and X-Forwarded-* headers are always untrusted. Production
	// transport is TLS and loopback development uses the direct peer address.
	if s.enrollmentLimiter == nil || !s.enrollmentLimiter.Allow(request.RemoteAddr, s.now().UTC()) {
		w.Header().Set("Retry-After", "30")
		writeProblem(w, http.StatusTooManyRequests, "enrollment.rate_limited")
		return
	}
	var input startEnrollmentRequest
	if err := decodeStrictRequest(request, maxEnrollmentBody, &input); err != nil {
		writeProblem(w, http.StatusBadRequest, "request.invalid")
		return
	}
	publicKey, err := decodeCanonical(input.PublicKey, ed25519.PublicKeySize)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "proof.invalid")
		return
	}
	defer clear(publicKey)
	requestNonce, err := decodeCanonical(input.RequestNonce, protocol.NonceBytes)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "proof.invalid")
		return
	}
	defer clear(requestNonce)
	signature, err := decodeCanonical(input.Signature, ed25519.SignatureSize)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "proof.invalid")
		return
	}
	defer clear(signature)
	now := s.now().UTC()
	if input.IssuedAt.Before(now.Add(-enrollmentClockSkew)) || input.IssuedAt.After(now.Add(enrollmentClockSkew)) ||
		!input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(enrollmentLifetime)) ||
		!input.ExpiresAt.After(input.IssuedAt) || input.ExpiresAt.Sub(input.IssuedAt) > enrollmentLifetime {
		writeProblem(w, http.StatusBadRequest, "proof.expired")
		return
	}
	transcript, err := protocol.EnrollmentTranscript(protocol.EnrollmentProof{
		ControllerID: input.ControllerID, KeyID: input.KeyID, PublicKey: input.PublicKey,
		InstallationID: input.InstallationID, RepositoryID: input.RepositoryID,
		RequestNonce: input.RequestNonce, IssuedAt: input.IssuedAt, ExpiresAt: input.ExpiresAt,
	})
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), transcript, signature) {
		clear(transcript)
		writeProblem(w, http.StatusUnauthorized, "proof.invalid")
		return
	}
	clear(transcript)

	enrollmentID, err := newRandomUUID(s.random)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "service.unavailable")
		return
	}
	state, err := randomBytes(s.random, 32)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "service.unavailable")
		return
	}
	defer clear(state)
	poll, err := randomBytes(s.random, 32)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "service.unavailable")
		return
	}
	defer clear(poll)
	verifier, err := randomToken(s.random, 32)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "service.unavailable")
		return
	}
	defer clear(verifier)
	aad := enrollmentAAD(enrollmentID, input.ControllerID, input.KeyID, input.InstallationID, input.RepositoryID, requestNonce)
	defer clear(aad)
	ciphertext, nonce, err := s.sealVerifier(verifier, aad)
	if err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "service.unavailable")
		return
	}
	defer clear(ciphertext)
	defer clear(nonce)
	storedID, err := s.store.CreateEnrollment(request.Context(), store.EnrollmentInput{
		EnrollmentID: enrollmentID, ControllerID: input.ControllerID, KeyID: input.KeyID,
		PublicKey: publicKey, InstallationID: input.InstallationID, RepositoryID: input.RepositoryID,
		StateHash: domainHash("rig.relay.v1/oauth-state", state), PollHash: domainHash("rig.relay.v1/enrollment-poll", poll),
		PKCECiphertext: ciphertext, PKCESealNonce: nonce, RequestNonce: requestNonce, ExpiresAt: input.ExpiresAt,
	})
	if err != nil {
		code := "store.unavailable"
		status := http.StatusServiceUnavailable
		if errors.Is(err, store.ErrReplay) {
			code, status = "proof.replay", http.StatusConflict
		} else if errors.Is(err, store.ErrCapacity) {
			w.Header().Set("Retry-After", "30")
			code, status = "enrollment.rate_limited", http.StatusTooManyRequests
		}
		writeProblem(w, status, code)
		return
	}
	if storedID != enrollmentID {
		writeProblem(w, http.StatusServiceUnavailable, "store.inconsistent")
		return
	}
	challenge := sha256.Sum256(verifier)
	authorize := githubWebOrigin + "/login/oauth/authorize?" + url.Values{
		"client_id":             {s.githubClientID},
		"redirect_uri":          {s.callbackURL()},
		"state":                 {base64.RawURLEncoding.EncodeToString(state)},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"repository_id":         {strconv.FormatInt(input.RepositoryID, 10)},
	}.Encode()
	writeJSON(w, http.StatusCreated, map[string]string{"authorizationUrl": authorize, "pollToken": base64.RawURLEncoding.EncodeToString(poll)})
}

func (s *Service) handleOAuthCallback(w http.ResponseWriter, request *http.Request) {
	setTerminalHeaders(w)
	if request.Method != http.MethodGet {
		writeTerminal(w, false)
		return
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeTerminal(w, false)
		return
	}
	states := query["state"]
	if len(states) != 1 {
		writeTerminal(w, false)
		return
	}
	state, err := decodeCanonical(states[0], 32)
	if err != nil {
		writeTerminal(w, false)
		return
	}
	defer clear(state)
	claim, err := s.store.ClaimEnrollmentState(request.Context(), domainHash("rig.relay.v1/oauth-state", state))
	if err != nil {
		writeTerminal(w, false)
		return
	}
	defer claim.Destroy()
	fail := func(code string) {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), enrollmentStoreTimeout)
		defer cancel()
		_ = s.store.FailEnrollment(cleanupContext, claim.EnrollmentID, code)
		writeTerminal(w, false)
	}
	// State is claimed before either provider error or code is examined. Query
	// installation_id is intentionally ignored; only the signed claim IDs are used.
	codes, errorsFromProvider := query["code"], query["error"]
	if (len(codes) != 1) == (len(errorsFromProvider) != 1) {
		fail("oauth.invalid")
		return
	}
	if len(errorsFromProvider) == 1 {
		if errorsFromProvider[0] == "" {
			fail("oauth.invalid")
			return
		}
		fail("oauth.denied")
		return
	}
	code := []byte(codes[0])
	defer clear(code)
	if len(code) == 0 {
		fail("oauth.invalid")
		return
	}
	aad := enrollmentAAD(claim.EnrollmentID, claim.ControllerID, claim.KeyID, claim.InstallationID, claim.RepositoryID, claim.RequestNonce)
	defer clear(aad)
	verifier, err := s.openVerifier(claim.PKCECiphertext, claim.PKCESealNonce, aad)
	if err != nil {
		fail("oauth.verifier")
		return
	}
	defer clear(verifier)
	tokens, err := s.exchangeOAuthCode(request.Context(), code, verifier, claim.RepositoryID)
	if err != nil {
		fail("oauth.exchange")
		return
	}
	defer tokens.Destroy()
	if err = s.verifyUserRepositoryAccess(request.Context(), tokens.access, claim.InstallationID, claim.RepositoryID); err != nil {
		fail("authorization.denied")
		return
	}
	completionContext, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), enrollmentStoreTimeout)
	err = s.store.CompleteEnrollment(completionContext, claim.EnrollmentID)
	cancel()
	if err != nil {
		fail("store.unavailable")
		return
	}
	writeTerminal(w, true)
}

func (s *Service) handlePollEnrollment(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeProblem(w, http.StatusMethodNotAllowed, "request.method")
		return
	}
	var input pollEnrollmentRequest
	if err := decodeStrictRequest(request, maxEnrollmentBody, &input); err != nil {
		writeProblem(w, http.StatusBadRequest, "request.invalid")
		return
	}
	poll, err := decodeCanonical(input.PollToken, 32)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "request.invalid")
		return
	}
	defer clear(poll)
	status, err := s.store.PollEnrollment(request.Context(), domainHash("rig.relay.v1/enrollment-poll", poll))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "enrollment.not_found")
		return
	}
	if err != nil && !errors.Is(err, store.ErrExpired) {
		writeProblem(w, http.StatusServiceUnavailable, "store.unavailable")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status      string     `json:"status"`
		FailureCode string     `json:"failureCode,omitempty"`
		CompletedAt *time.Time `json:"completedAt,omitempty"`
	}{Status: status.Status, FailureCode: status.FailureCode, CompletedAt: status.CompletedAt})
}

func (s *Service) callbackURL() string {
	base := *s.publicBaseURL
	base.Path = oauthCallbackPath
	return base.String()
}

func (s *Service) sealVerifier(verifier, aad []byte) ([]byte, []byte, error) {
	aead, err := s.enrollmentAEAD()
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(s.random, nonce); err != nil {
		clear(nonce)
		return nil, nil, err
	}
	return aead.Seal(nil, nonce, verifier, aad), nonce, nil
}

func (s *Service) openVerifier(ciphertext, nonce, aad []byte) ([]byte, error) {
	aead, err := s.enrollmentAEAD()
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ciphertext, aad)
}

func (s *Service) enrollmentAEAD() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.enrollmentKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func enrollmentAAD(enrollmentID, controllerID, keyID string, installationID, repositoryID int64, requestNonce []byte) []byte {
	fields := [][]byte{[]byte("rig.relay.v1/enrollment-verifier"), []byte(enrollmentID), []byte(controllerID), []byte(keyID), uint64Bytes(uint64(installationID)), uint64Bytes(uint64(repositoryID)), requestNonce}
	var aad []byte
	for _, field := range fields {
		aad = append(aad, uint64Bytes(uint64(len(field)))...)
		aad = append(aad, field...)
	}
	return aad
}

func uint64Bytes(value uint64) []byte {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	return encoded[:]
}

func domainHash(domain string, value []byte) []byte {
	hash := sha256.New()
	_, _ = hash.Write(uint64Bytes(uint64(len(domain))))
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write(uint64Bytes(uint64(len(value))))
	_, _ = hash.Write(value)
	return hash.Sum(nil)
}

func randomToken(random io.Reader, size int) ([]byte, error) {
	raw, err := randomBytes(random, size)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(raw)))
	base64.RawURLEncoding.Encode(encoded, raw)
	return encoded, nil
}

func randomBytes(random io.Reader, size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(random, value); err != nil {
		clear(value)
		return nil, err
	}
	return value, nil
}

func newRandomUUID(random io.Reader) (string, error) {
	id, err := uuid.NewRandomFromReader(random)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func decodeCanonical(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != size || base64.RawURLEncoding.EncodeToString(decoded) != value {
		clear(decoded)
		return nil, errors.New("invalid base64url value")
	}
	return decoded, nil
}

func decodeStrictRequest(request *http.Request, limit int64, target any) error {
	if request.Body == nil || request.Header.Get("Content-Type") != "application/json" {
		return errors.New("invalid content type")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil || int64(len(body)) > limit || len(body) == 0 {
		clear(body)
		return errors.New("invalid body")
	}
	defer clear(body)
	if err := rejectDuplicateKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func rejectDuplicateKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON key")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate JSON key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeProblem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code})
}

func setTerminalHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeTerminal(w http.ResponseWriter, success bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if success {
		_, _ = io.WriteString(w, "<!doctype html><title>Connected</title><p>GitHub repository connected. You may close this window.</p>")
		return
	}
	_, _ = io.WriteString(w, "<!doctype html><title>Not connected</title><p>GitHub repository connection failed. Return to the client and try again.</p>")
}
