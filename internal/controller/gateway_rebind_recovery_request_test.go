package controller

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
)

func TestLANGatewayRebindRecoveryReadsRequireExactOperation(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			for _, mode := range []string{"valid", "missing operation", "noncanonical operation", "query", "empty query", "wrong method"} {
				t.Run(mode, func(t *testing.T) {
					body := ""
					read := readLANGatewayRebindStatusRequest
					if method == http.MethodPost {
						body, read = "{}", readLANGatewayRebindRecoveryPreviewRequest
					}
					request := httptest.NewRequest(method, "/api/v1/system/lan-gateway-rebind/"+rebindRequestOperation, strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					request.SetPathValue("operationId", rebindRequestOperation)
					switch mode {
					case "missing operation":
						request.SetPathValue("operationId", "")
					case "noncanonical operation":
						request.SetPathValue("operationId", "11111111111141118111111111111111")
					case "query":
						request.URL.RawQuery = "operationId=" + rebindRequestRevision
					case "empty query":
						request.URL.ForceQuery = true
					case "wrong method":
						request.Method = http.MethodPut
					}
					operationID, err := read(request)
					if mode == "valid" {
						if err != nil || operationID != rebindRequestOperation {
							t.Fatalf("exact path was not preserved: id=%q err=%v", operationID, err)
						}
					} else if operationID != "" || !errors.Is(err, appaccess.ErrInvalidInput) {
						t.Fatalf("ambiguous operation accepted: id=%q err=%v", operationID, err)
					}
				})
			}
		})
	}
}

func TestLANGatewayRebindRecoveryReadsRejectBodyAuthority(t *testing.T) {
	for _, fixture := range []struct {
		name, method, body string
	}{
		{"GET JSON", http.MethodGet, "{}"},
		{"GET whitespace", http.MethodGet, " "},
		{"preview operation", http.MethodPost, `{"operationId":"` + rebindRequestRevision + `"}`},
		{"preview policy", http.MethodPost, `{"policy":"rollback_only"}`},
		{"preview actor", http.MethodPost, `{"actorId":"administrator"}`},
		{"preview null", http.MethodPost, `null`},
		{"preview trailing object", http.MethodPost, `{} {}`},
		{"preview oversize", http.MethodPost, strings.Repeat(" ", maxGatewayRebindRequestBodyBytes+1)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			request := httptest.NewRequest(fixture.method, "/api/v1/system/lan-gateway-rebind/"+rebindRequestOperation, nil)
			request.Body = io.NopCloser(strings.NewReader(fixture.body))
			request.ContentLength = 0
			request.Header.Set("Content-Type", "application/json")
			request.SetPathValue("operationId", rebindRequestOperation)
			read := readLANGatewayRebindStatusRequest
			if fixture.method == http.MethodPost {
				read = readLANGatewayRebindRecoveryPreviewRequest
			}
			operationID, err := read(request)
			if operationID != "" || err == nil {
				t.Fatalf("body supplied status/preview authority: id=%q err=%v", operationID, err)
			}
		})
	}
}
