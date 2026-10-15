package controller

import (
	"io"
	"net/http"

	"github.com/hostd/hostd/internal/appaccess"
)

func readLANGatewayRebindOperationPath(r *http.Request, method string) (string, error) {
	if r == nil || r.Method != method || r.URL == nil || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return "", appaccess.ErrInvalidInput
	}
	operationID := r.PathValue("operationId")
	if !validCanonicalUUID(operationID) {
		return "", appaccess.ErrInvalidInput
	}
	return operationID, nil
}

func readLANGatewayRebindStatusRequest(r *http.Request) (string, error) {
	operationID, err := readLANGatewayRebindOperationPath(r, http.MethodGet)
	if err != nil {
		return "", err
	}
	if err := readLANGatewayRebindStatusBody(r); err != nil {
		return "", err
	}
	return operationID, nil
}

func readLANGatewayRebindStatusBody(r *http.Request) error {
	if len(r.Header.Values("Content-Encoding")) != 0 {
		return appaccess.ErrInvalidInput
	}
	if r.Body != nil {
		defer r.Body.Close()
		// Content-Length alone cannot prove an empty body, including for tests
		// and adapters that construct requests without an HTTP server parser.
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 1))
		if readErr != nil || len(body) != 0 {
			return appaccess.ErrInvalidInput
		}
	}
	return nil
}

func readLANGatewayRebindRecoveryPreviewRequest(r *http.Request) (string, error) {
	operationID, err := readLANGatewayRebindOperationPath(r, http.MethodPost)
	if err != nil {
		return "", err
	}
	if err := readLANGatewayRebindRecoveryPreviewBody(r); err != nil {
		return "", err
	}
	return operationID, nil
}

func readLANGatewayRebindRecoveryPreviewBody(r *http.Request) error {
	// The operation is supplied exclusively by the matched path. The preview
	// accepts an empty object, never client-selected policy, state or authority.
	if _, err := readLANGatewayRebindFields(r); err != nil {
		return err
	}
	return nil
}
