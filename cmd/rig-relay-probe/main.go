// rig-relay-probe performs a bounded HTTPS health or readiness check against
// the relay. It deliberately emits no request, response, certificate, or
// network details because container health output may be retained by runtimes.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	maximumResponseBytes = 32
	maximumCABytes       = 1 << 20
	minimumTimeout       = 10 * time.Millisecond
	maximumTimeout       = 10 * time.Second
)

type probeOptions struct {
	baseURL    *url.URL
	serverName string
	caFile     string
	endpoint   string
	timeout    time.Duration
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	options, err := parseOptions(args)
	if err == nil {
		err = probe(options)
	}
	if err != nil {
		_, _ = io.WriteString(stderr, "relay probe failed\n")
		return 1
	}
	_, _ = io.WriteString(stdout, "relay probe ok\n")
	return 0
}

func parseOptions(args []string) (probeOptions, error) {
	set := flag.NewFlagSet("rig-relay-probe", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	base := set.String("base-url", "", "HTTPS relay origin")
	serverName := set.String("server-name", "", "expected TLS DNS name")
	caFile := set.String("ca-file", "", "optional PEM CA bundle")
	endpoint := set.String("endpoint", "", "health or ready")
	timeout := set.Duration("timeout", 5*time.Second, "total probe timeout")
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		return probeOptions{}, errors.New("invalid arguments")
	}
	if *endpoint != "health" && *endpoint != "ready" {
		return probeOptions{}, errors.New("invalid endpoint")
	}
	if *timeout < minimumTimeout || *timeout > maximumTimeout {
		return probeOptions{}, errors.New("invalid timeout")
	}
	if !validDNSName(*serverName) {
		return probeOptions{}, errors.New("invalid server name")
	}
	parsed, err := url.Parse(*base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery || parsed.RawPath != "" || (parsed.Path != "" && parsed.Path != "/") {
		return probeOptions{}, errors.New("invalid base URL")
	}
	if parsed.Hostname() == "" {
		return probeOptions{}, errors.New("invalid base URL host")
	}
	if port := parsed.Port(); port != "" {
		value, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || value == 0 {
			return probeOptions{}, errors.New("invalid base URL port")
		}
	}
	return probeOptions{baseURL: parsed, serverName: *serverName, caFile: *caFile, endpoint: *endpoint, timeout: *timeout}, nil
}

func validDNSName(value string) bool {
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func probe(options probeOptions) error {
	rootCAs, err := loadRoots(options.caFile)
	if err != nil {
		return err
	}
	target := *options.baseURL
	if options.endpoint == "health" {
		target.Path = "/healthz"
	} else {
		target.Path = "/readyz"
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxIdleConns:           0,
		MaxConnsPerHost:        1,
		ResponseHeaderTimeout:  options.timeout,
		MaxResponseHeaderBytes: 16 << 10,
		TLSHandshakeTimeout:    options.timeout,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    rootCAs,
			ServerName: options.serverName,
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   options.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirect refused")
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return errors.New("request invalid")
	}
	request.Header.Set("User-Agent", "rig-relay-probe/1")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil || len(body) > maximumResponseBytes || response.StatusCode != http.StatusOK {
		return errors.New("response invalid")
	}
	want := "ready\n"
	if options.endpoint == "health" {
		want = "ok\n"
	}
	if string(body) != want {
		return errors.New("response invalid")
	}
	return nil
}

func loadRoots(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumCABytes {
		return nil, errors.New("CA file invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("CA file invalid")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumCABytes+1))
	if err != nil || len(data) == 0 || len(data) > maximumCABytes {
		return nil, errors.New("CA file invalid")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("CA file invalid")
	}
	return pool, nil
}
