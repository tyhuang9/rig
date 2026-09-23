package projectanalysis

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func validateStrictJSON(body []byte, maxDepth int) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := readJSONValue(decoder, 0, maxDepth); err != nil {
		var validationError *jsonValidationError
		if errors.As(err, &validationError) {
			return validationError.code, err
		}
		return "malformed_package_json", err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return "malformed_package_json", fmt.Errorf("unexpected trailing token %v", token)
		}
		return "malformed_package_json", err
	}
	return "", nil
}

type jsonValidationError struct {
	code string
	err  error
}

func (e *jsonValidationError) Error() string { return e.err.Error() }
func (e *jsonValidationError) Unwrap() error { return e.err }

func readJSONValue(decoder *json.Decoder, depth, maxDepth int) error {
	if depth > maxDepth {
		return &jsonValidationError{code: "json_too_deep", err: errors.New("JSON nesting exceeds limit")}
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := keys[key]; exists {
				return &jsonValidationError{code: "duplicate_json_key", err: fmt.Errorf("duplicate object key %q", key)}
			}
			keys[key] = struct{}{}
			if err := readJSONValue(decoder, depth+1, maxDepth); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := readJSONValue(decoder, depth+1, maxDepth); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("array is not closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}
