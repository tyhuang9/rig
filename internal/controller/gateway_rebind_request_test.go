package controller

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hostd/hostd/internal/appaccess"
	"github.com/hostd/hostd/internal/generatedingress"
)

const (
	rebindRequestOperation = "11111111-1111-4111-8111-111111111111"
	rebindRequestRevision  = "22222222-2222-4222-8222-222222222222"
	rebindProfileOperation = "33333333-3333-4333-8333-333333333333"
)

type rebindRequestFixture struct {
	name     string
	body     string
	expected any
	read     func(*http.Request) (any, error)
}

func rebindRequestFixtures() []rebindRequestFixture {
	proposal := `{"operationId":"` + rebindRequestOperation + `","successorProfileRevisionId":"` + rebindRequestRevision + `","successorProfileRevisionNumber":2,"successorProfileOperationId":"` + rebindProfileOperation + `","successorProfile":{"interfaceId":"lan-interface","selectedIpv4":"192.168.10.20","portStart":8100,"portEnd":8119}}`
	input := generatedingress.GatewayRebindProposalInput{
		OperationID: rebindRequestOperation, SuccessorProfileRevisionID: rebindRequestRevision,
		SuccessorProfileRevisionNumber: 2, SuccessorProfileOperationID: rebindProfileOperation,
		SuccessorProfile: appaccess.GatewayProfileSpec{InterfaceID: "lan-interface", SelectedIPv4: "192.168.10.20", PortStart: 8100, PortEnd: 8119},
	}
	return []rebindRequestFixture{
		{"proposal", proposal, input, func(r *http.Request) (any, error) { return readLANGatewayRebindProposalRequest(r) }},
		{"commit", strings.TrimSuffix(proposal, "}") + `,"version":1,"proposalDigest":"` + strings.Repeat("a", 64) + `","configureApprovalDigest":"` + strings.Repeat("b", 64) + `","rebindApprovalDigest":"` + strings.Repeat("c", 64) + `"}`,
			lanGatewayRebindCommitRequest{Version: 1, Proposal: input, ProposalDigest: strings.Repeat("a", 64), ConfigureApprovalDigest: strings.Repeat("b", 64), RebindApprovalDigest: strings.Repeat("c", 64)},
			func(r *http.Request) (any, error) { return readLANGatewayRebindCommitRequest(r) }},
		{"reconcile", `{"version":1,"action":"reconcile","recoveryDigest":"` + strings.Repeat("d", 64) + `","recoveryApprovalDigest":"` + strings.Repeat("e", 64) + `"}`,
			lanGatewayRebindReconcileRequest{Version: 1, OperationID: rebindRequestOperation, Action: "reconcile", RecoveryDigest: strings.Repeat("d", 64), RecoveryApprovalDigest: strings.Repeat("e", 64)},
			func(r *http.Request) (any, error) {
				return readLANGatewayRebindReconcileRequest(r, rebindRequestOperation)
			}},
	}
}

func newRebindReadRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/system/lan-gateway-rebind/"+rebindRequestOperation+"/reconcile", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	return r
}

func TestLANGatewayRebindRequestsPreserveReviewedInputs(t *testing.T) {
	for _, fixture := range rebindRequestFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			body := &rebindTrackingBody{Reader: strings.NewReader(" \n" + fixture.body + "\n ")}
			r := newRebindReadRequest("")
			r.Body = body
			got, err := fixture.read(r)
			if err != nil || !reflect.DeepEqual(got, fixture.expected) || !body.closed {
				t.Fatalf("decoded=%#v err=%v closed=%v", got, err, body.closed)
			}
		})
	}
}

func TestLANGatewayRebindRequestsRejectAmbiguousInput(t *testing.T) {
	for _, fixture := range rebindRequestFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			bodies := map[string]string{
				"empty": "", "null": "null", "array": "[]", "extra document": fixture.body + " {}",
				"trailing junk": fixture.body + " junk", "invalid UTF8": fixture.body + "\xff",
				"actor injection": addRebindField(fixture.body, `"actorId":"other-admin"`),
				"force":           addRebindField(fixture.body, `"force":true`),
				"rollback":        addRebindField(fixture.body, `"rollback":true`),
				"forward":         addRebindField(fixture.body, `"rollForward":true`),
				"physical proof":  addRebindField(fixture.body, `"physicalProof":{}`),
			}
			if fixture.name == "reconcile" {
				bodies["duplicate"] = addRebindField(fixture.body, `"version":1`)
				bodies["escaped duplicate"] = addRebindField(fixture.body, `"ver\u0073ion":1`)
				bodies["path override"] = addRebindField(fixture.body, `"operationId":"`+rebindProfileOperation+`"`)
				bodies["redundant path ID"] = addRebindField(fixture.body, `"operationId":"`+rebindRequestOperation+`"`)
				bodies["missing required"] = strings.Replace(fixture.body, `"action":"reconcile",`, "", 1)
			} else {
				bodies["duplicate"] = addRebindField(fixture.body, `"operationId":"`+rebindRequestOperation+`"`)
				bodies["escaped duplicate"] = addRebindField(fixture.body, `"operati\u006fnId":"`+rebindRequestOperation+`"`)
				bodies["duplicate nested"] = strings.Replace(fixture.body, `"portStart":8100`, `"portStart":8100,"portStart":8101`, 1)
				bodies["unknown nested"] = strings.Replace(fixture.body, `"portStart":8100`, `"portStart":8100,"actorId":"other-admin"`, 1)
				bodies["invalid UTF8 interface"] = strings.Replace(fixture.body, "lan-interface", "lan-\xff-interface", 1)
				bodies["missing required"] = strings.Replace(fixture.body, `"successorProfileRevisionNumber":2,`, "", 1)
			}
			for name, body := range bodies {
				t.Run(name, func(t *testing.T) { assertRebindRequestInvalid(t, fixture, newRebindReadRequest(body)) })
			}
			for name, mutate := range map[string]func(*http.Request){
				"query":                func(r *http.Request) { r.URL.RawQuery = "force=true" },
				"duplicate query":      func(r *http.Request) { r.URL.RawQuery = "version=1&version=2" },
				"bare query":           func(r *http.Request) { r.URL.ForceQuery = true },
				"wrong method":         func(r *http.Request) { r.Method = http.MethodGet },
				"missing media type":   func(r *http.Request) { r.Header.Del("Content-Type") },
				"wrong media type":     func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
				"duplicate media type": func(r *http.Request) { r.Header.Add("Content-Type", "application/json") },
				"encoded body":         func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
			} {
				t.Run(name, func(t *testing.T) {
					r := newRebindReadRequest(fixture.body)
					mutate(r)
					assertRebindRequestInvalid(t, fixture, r)
				})
			}
		})
	}
}

func TestLANGatewayRebindRequestsBoundBody(t *testing.T) {
	for _, fixture := range rebindRequestFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			for _, extra := range []int{0, 1, 4096} {
				body := &rebindTrackingBody{Reader: strings.NewReader(fixture.body + strings.Repeat(" ", maxGatewayRebindRequestBodyBytes-len(fixture.body)+extra))}
				r := newRebindReadRequest("")
				r.Body = body
				r.ContentLength = -1 // The actual stream, including chunked input, sets the limit.
				got, err := fixture.read(r)
				if extra == 0 {
					if err != nil || !reflect.DeepEqual(got, fixture.expected) {
						t.Fatalf("exact limit rejected: %v", err)
					}
				} else {
					var tooLarge *http.MaxBytesError
					if !errors.As(err, &tooLarge) || tooLarge.Limit != maxGatewayRebindRequestBodyBytes || !reflect.ValueOf(got).IsZero() {
						t.Fatalf("oversize did not return zero input and typed size error: %#v %v", got, err)
					}
				}
				if !body.closed || body.read > maxGatewayRebindRequestBodyBytes+1 {
					t.Fatalf("unbounded or unclosed body: read=%d closed=%v", body.read, body.closed)
				}
			}
			r := newRebindReadRequest("")
			r.Body = &rebindTrackingBody{Reader: rebindFailingReader{}}
			assertRebindRequestInvalid(t, fixture, r)
			if !r.Body.(*rebindTrackingBody).closed {
				t.Fatal("failed input stream was not closed")
			}
		})
	}
}

func TestLANGatewayRebindRequestsValidateCanonicalValues(t *testing.T) {
	for _, fixture := range rebindRequestFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			changes := map[string][2]string{}
			if fixture.name != "reconcile" {
				changes = map[string][2]string{
					"noncanonical ID":      {rebindRequestOperation, "{" + rebindRequestOperation + "}"},
					"same operation":       {rebindProfileOperation, rebindRequestOperation},
					"zero revision":        {`"successorProfileRevisionNumber":2`, `"successorProfileRevisionNumber":0`},
					"overflow revision":    {`"successorProfileRevisionNumber":2`, `"successorProfileRevisionNumber":9223372036854775808`},
					"null revision":        {`"successorProfileRevisionNumber":2`, `"successorProfileRevisionNumber":null`},
					"fraction revision":    {`"successorProfileRevisionNumber":2`, `"successorProfileRevisionNumber":2.5`},
					"public IPv4":          {"192.168.10.20", "8.8.8.8"},
					"wildcard IPv4":        {"192.168.10.20", "0.0.0.0"},
					"IPv6":                 {"192.168.10.20", "fd00::1"},
					"blank interface":      {"lan-interface", " "},
					"interface whitespace": {"lan-interface", " lan-interface"},
					"large interface":      {"lan-interface", strings.Repeat("x", 513)},
					"negative port":        {`"portStart":8100`, `"portStart":-1`},
					"overflow port":        {`"portStart":8100`, `"portStart":65536`},
					"pool escape":          {`"portStart":8100`, `"portStart":80`},
					"reverse pool":         {`"portStart":8100`, `"portStart":8120`},
					"port string":          {`"portStart":8100`, `"portStart":"8100"`},
					"null interface":       {`"interfaceId":"lan-interface"`, `"interfaceId":null`},
				}
			}
			if fixture.name != "proposal" {
				changes["unknown version"] = [2]string{`"version":1`, `"version":2`}
				changes["null version"] = [2]string{`"version":1`, `"version":null`}
				changes["version string"] = [2]string{`"version":1`, `"version":"1"`}
			}
			if fixture.name == "commit" {
				for _, value := range []string{"a", "b", "c"} {
					changes["uppercase digest "+value] = [2]string{strings.Repeat(value, 64), strings.Repeat(strings.ToUpper(value), 64)}
					changes["short digest "+value] = [2]string{strings.Repeat(value, 64), strings.Repeat(value, 63)}
				}
			}
			if fixture.name == "reconcile" {
				changes["rollback action"] = [2]string{`"reconcile"`, `"rollback"`}
				changes["forward action"] = [2]string{`"reconcile"`, `"rollForward"`}
				changes["null action"] = [2]string{`"action":"reconcile"`, `"action":null`}
				for _, value := range []string{"d", "e"} {
					changes["uppercase digest "+value] = [2]string{strings.Repeat(value, 64), strings.Repeat(strings.ToUpper(value), 64)}
					changes["short digest "+value] = [2]string{strings.Repeat(value, 64), strings.Repeat(value, 63)}
				}
			}
			for name, change := range changes {
				t.Run(name, func(t *testing.T) {
					if !strings.Contains(fixture.body, change[0]) {
						t.Fatal("invalid test mutation")
					}
					assertRebindRequestInvalid(t, fixture, newRebindReadRequest(strings.Replace(fixture.body, change[0], change[1], 1)))
				})
			}
			if fixture.name == "reconcile" {
				got, err := readLANGatewayRebindReconcileRequest(newRebindReadRequest(fixture.body), "not-an-operation")
				if !errors.Is(err, appaccess.ErrInvalidInput) || !reflect.ValueOf(got).IsZero() {
					t.Fatalf("invalid path accepted: %#v %v", got, err)
				}
			}
		})
	}
}

func assertRebindRequestInvalid(t *testing.T, fixture rebindRequestFixture, r *http.Request) {
	t.Helper()
	got, err := fixture.read(r)
	if !errors.Is(err, appaccess.ErrInvalidInput) || !reflect.ValueOf(got).IsZero() || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("invalid request returned value or unsafe error: %#v %v", got, err)
	}
}

func addRebindField(body, field string) string {
	return strings.TrimSuffix(body, "}") + "," + field + "}"
}

type rebindTrackingBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *rebindTrackingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *rebindTrackingBody) Close() error { b.closed = true; return nil }

type rebindFailingReader struct{}

func (rebindFailingReader) Read([]byte) (int, error) { return 0, errors.New("sensitive reader error") }
