package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscovererLatestSelectsExactReleaseAssets(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != latestReleasePath {
			t.Errorf("request path = %q, want %q", request.URL.Path, latestReleasePath)
		}
		if request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept header = %q", request.Header.Get("Accept"))
		}
		if request.Header.Get("User-Agent") != "tao-self-update" {
			t.Errorf("User-Agent header = %q", request.Header.Get("User-Agent"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{
			"tag_name":"v1.12.3",
			"draft":false,
			"prerelease":false,
			"assets":[
				{"name":"tao_1.12.3_darwin_arm64.tar.gz","browser_download_url":%q},
				{"name":"tao_1.12.3_linux_arm64.tar.gz.sig","browser_download_url":%q},
				{"name":"checksums.txt.bak","browser_download_url":%q},
				{"name":"checksums.txt","browser_download_url":%q},
				{"name":"tao_1.12.3_linux_arm64.tar.gz","browser_download_url":%q}
			]
		}`, server.URL+"/wrong-os", server.URL+"/signature", server.URL+"/wrong-checksums", server.URL+"/checksums.txt", server.URL+"/archive")
	}))
	defer server.Close()

	discoverer := Discoverer{
		HTTPClient: server.Client(),
		APIBaseURL: server.URL + "/",
		GOOS:       "linux",
		GOARCH:     "arm64",
	}
	release, err := discoverer.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if release.Tag != "v1.12.3" {
		t.Errorf("Tag = %q, want v1.12.3", release.Tag)
	}
	if release.Checksums != (Asset{Name: "checksums.txt", URL: server.URL + "/checksums.txt"}) {
		t.Errorf("Checksums = %#v", release.Checksums)
	}
	wantArchive := Asset{Name: "tao_1.12.3_linux_arm64.tar.gz", URL: server.URL + "/archive"}
	if release.Archive != wantArchive {
		t.Errorf("Archive = %#v, want %#v", release.Archive, wantArchive)
	}
}

func TestDiscovererLatestRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		wantError  string
	}{
		{name: "HTTP failure", statusCode: http.StatusBadGateway, body: `{}`, wantError: "502 Bad Gateway"},
		{name: "malformed JSON", statusCode: http.StatusOK, body: `{`, wantError: "decode response"},
		{name: "draft", statusCode: http.StatusOK, body: releaseJSON("v1.2.3", true, false, validAssets()), wantError: "draft"},
		{name: "prerelease", statusCode: http.StatusOK, body: releaseJSON("v1.2.3", false, true, validAssets()), wantError: "prerelease"},
		{name: "prerelease tag", statusCode: http.StatusOK, body: releaseJSON("v1.2.3-rc.1", false, false, validAssets()), wantError: "invalid release tag"},
		{name: "missing tag", statusCode: http.StatusOK, body: releaseJSON("", false, false, validAssets()), wantError: "invalid release tag"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.statusCode)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			discoverer := Discoverer{HTTPClient: server.Client(), APIBaseURL: server.URL, GOOS: "linux", GOARCH: "amd64"}
			_, err := discoverer.Latest(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Errorf("Latest() error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestDiscovererLatestBoundsResponseSize(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", maxReleaseResponseBytes+1)))
	}))
	defer server.Close()

	discoverer := Discoverer{HTTPClient: server.Client(), APIBaseURL: server.URL, GOOS: "darwin", GOARCH: "amd64"}
	_, err := discoverer.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("Latest() error = %v, want oversized response error", err)
	}
}

func TestDiscovererLatestRequiresUniqueExactAssets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		assets    string
		wantError string
	}{
		{name: "missing checksums", assets: assetJSON("tao_1.2.3_linux_amd64.tar.gz", "https://example.com/archive"), wantError: `asset "checksums.txt" is missing`},
		{name: "duplicate checksums", assets: strings.Join([]string{assetJSON("checksums.txt", "https://example.com/a"), assetJSON("checksums.txt", "https://example.com/b"), assetJSON("tao_1.2.3_linux_amd64.tar.gz", "https://example.com/archive")}, ","), wantError: `asset "checksums.txt" is duplicated`},
		{name: "missing archive", assets: assetJSON("checksums.txt", "https://example.com/checksums"), wantError: `asset "tao_1.2.3_linux_amd64.tar.gz" is missing`},
		{name: "duplicate archive", assets: strings.Join([]string{assetJSON("checksums.txt", "https://example.com/checksums"), assetJSON("tao_1.2.3_linux_amd64.tar.gz", "https://example.com/a"), assetJSON("tao_1.2.3_linux_amd64.tar.gz", "https://example.com/b")}, ","), wantError: `asset "tao_1.2.3_linux_amd64.tar.gz" is duplicated`},
		{name: "invalid asset URL", assets: strings.Join([]string{assetJSON("checksums.txt", "/relative"), assetJSON("tao_1.2.3_linux_amd64.tar.gz", "https://example.com/archive")}, ","), wantError: "invalid download URL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(writer, releaseJSON("v1.2.3", false, false, test.assets))
			}))
			defer server.Close()

			discoverer := Discoverer{HTTPClient: server.Client(), APIBaseURL: server.URL, GOOS: "linux", GOARCH: "amd64"}
			_, err := discoverer.Latest(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Errorf("Latest() error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestDiscovererLatestTimesOutStalledServerWithoutCallerDeadline(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	requestDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
		close(requestDone)
	}))
	defer server.Close()

	discoverer := Discoverer{
		HTTPClient:     server.Client(),
		APIBaseURL:     server.URL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		RequestTimeout: 50 * time.Millisecond,
	}
	_, err := discoverer.Latest(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Latest() error = %v, want deadline exceeded", err)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("stalled server did not receive request")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("request context was not canceled after discovery timeout")
	}
}

func TestDiscovererRequestContextHasFiniteDefaultTimeout(t *testing.T) {
	t.Parallel()

	started := time.Now()
	ctx, cancel := (&Discoverer{}).requestContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("request context has no deadline")
	}
	if deadline.Before(started) || deadline.After(started.Add(defaultDiscoveryTimeout+time.Second)) {
		t.Errorf("request deadline = %v, want finite default timeout near %v", deadline, defaultDiscoveryTimeout)
	}
}

func TestDiscovererLatestHonorsCancellation(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	discoverer := Discoverer{HTTPClient: server.Client(), APIBaseURL: server.URL, GOOS: "linux", GOARCH: "amd64"}
	result := make(chan error, 1)
	go func() {
		_, err := discoverer.Latest(ctx)
		result <- err
	}()
	<-requestStarted
	cancel()

	err := <-result
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("Latest() error = %v, want context cancellation", err)
	}
}

func TestDiscovererLatestRejectsUnsupportedTargetBeforeRequest(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	for _, target := range []struct {
		goos   string
		goarch string
	}{
		{goos: "windows", goarch: "amd64"},
		{goos: "linux", goarch: "386"},
	} {
		discoverer := Discoverer{HTTPClient: server.Client(), APIBaseURL: server.URL, GOOS: target.goos, GOARCH: target.goarch}
		_, err := discoverer.Latest(context.Background())
		if err == nil || !strings.Contains(err.Error(), "unsupported on "+target.goos+"/"+target.goarch) {
			t.Errorf("Latest() for %s/%s error = %v", target.goos, target.goarch, err)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("server received %d requests, want 0", got)
	}
}

func validAssets() string {
	return strings.Join([]string{
		assetJSON("checksums.txt", "https://example.com/checksums.txt"),
		assetJSON("tao_1.2.3_linux_amd64.tar.gz", "https://example.com/archive"),
	}, ",")
}

func releaseJSON(tag string, draft, prerelease bool, assets string) string {
	return fmt.Sprintf(`{"tag_name":%q,"draft":%t,"prerelease":%t,"assets":[%s]}`, tag, draft, prerelease, assets)
}

func assetJSON(name, downloadURL string) string {
	return fmt.Sprintf(`{"name":%q,"browser_download_url":%q}`, name, downloadURL)
}
