package controllerrelay

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const relayControllerSessionPath = "/v1/controllers/connect"

func newRelayHTTPClient(transport http.RoundTripper, timeout time.Duration) *http.Client {
	if transport == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
		transport = &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			ForceAttemptHTTP2:      true,
			MaxIdleConns:           16,
			MaxIdleConnsPerHost:    4,
			IdleConnTimeout:        30 * time.Second,
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  10 * time.Second,
			ExpectContinueTimeout:  time.Second,
			MaxResponseHeaderBytes: 16 << 10,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		}
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func parseCanonicalHTTPSOrigin(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > 2048 || strings.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("relay HTTPS origin is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("relay origin must be an exact HTTPS origin")
	}
	if parsed.String() != raw || parsed.Host != strings.ToLower(parsed.Host) || strings.Contains(parsed.Host, "%") {
		return nil, errors.New("relay HTTPS origin is not canonical")
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.HasSuffix(hostname, ".") || strings.IndexFunc(hostname, func(r rune) bool { return r <= 0x20 || r >= 0x7f }) >= 0 {
		return nil, errors.New("relay HTTPS origin host is invalid")
	}
	port := parsed.Port()
	if port == "443" {
		return nil, errors.New("relay HTTPS origin contains a default port")
	}
	if port != "" {
		value, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || value == 0 || strconv.FormatUint(value, 10) != port {
			return nil, errors.New("relay HTTPS origin port is invalid")
		}
	}
	if strings.Contains(hostname, ":") {
		ip := net.ParseIP(hostname)
		if ip == nil || "["+ip.String()+"]"+optionalPort(port) != parsed.Host {
			return nil, errors.New("relay HTTPS origin IPv6 host is not canonical")
		}
	}
	return parsed, nil
}

// ParseCanonicalHTTPSOrigin validates a relay origin without normalizing it.
// Callers that persist or dial a relay endpoint must use this exact seam so an
// accepted value is always the same canonical HTTPS origin the transport uses.
func ParseCanonicalHTTPSOrigin(raw string) (*url.URL, error) {
	return parseCanonicalHTTPSOrigin(raw)
}

func relayControllerSessionURL(origin *url.URL) (string, error) {
	if origin == nil {
		return "", errors.New("relay HTTPS origin is required")
	}
	target := *origin
	target.Path = relayControllerSessionPath
	return target.String(), nil
}

func optionalPort(port string) string {
	if port == "" {
		return ""
	}
	return ":" + port
}
