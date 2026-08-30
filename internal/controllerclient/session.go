package controllerclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hostd/hostd/internal/secretfile"
)

const SessionFilePurpose = "hostctl-session"
const maxSessionFileBytes int64 = 64 << 10

func DefaultSessionFile() string {
	root, err := os.UserConfigDir()
	if err != nil {
		root = "."
	}
	return filepath.Join(root, "hostd", "hostctl-session.json")
}
func WriteSessionFile(path string, session Session) error {
	if session.SessionToken == "" || session.CSRFToken == "" {
		return errors.New("session credentials are incomplete")
	}
	payload, err := json.Marshal(session)
	if err != nil {
		return err
	}
	defer clear(payload)
	if err := secretfile.Write(path, SessionFilePurpose, payload); err != nil {
		return fmt.Errorf("protect session file: %w", err)
	}
	return nil
}
func ReadSessionFile(path string) (Session, error) {
	var session Session
	payload, err := secretfile.Read(path, SessionFilePurpose)
	if err != nil {
		return session, fmt.Errorf("read session file: %w", err)
	}
	defer clear(payload)
	if err := decodeSession(bytes.NewReader(payload), &session); err != nil {
		return session, fmt.Errorf("decode session file: %w", err)
	}
	return session, nil
}
func ReadSession(r io.Reader) (Session, error) {
	var s Session
	err := decodeSession(r, &s)
	return s, err
}
func decodeSession(r io.Reader, target *Session) error {
	d := json.NewDecoder(io.LimitReader(r, maxSessionFileBytes+1))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("input must contain one JSON object")
	}
	if target.SessionToken == "" || target.CSRFToken == "" {
		return errors.New("session credentials are incomplete")
	}
	return nil
}
func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
