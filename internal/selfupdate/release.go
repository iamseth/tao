package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

const (
	defaultAPIBaseURL       = "https://api.github.com"
	latestReleasePath       = "/repos/iamseth/tao/releases/latest"
	maxReleaseResponseBytes = 1 << 20
	defaultDiscoveryTimeout = 10 * time.Second
)

// Asset identifies a required file attached to a GitHub release.
type Asset struct {
	Name string
	URL  string
}

// Release is a validated stable Tao release for one supported platform.
type Release struct {
	Tag       string
	Checksums Asset
	Archive   Asset
}

// Discoverer locates the latest stable Tao release. APIBaseURL, GOOS,
// GOARCH, and RequestTimeout are injectable for hermetic tests; empty values
// use production defaults.
type Discoverer struct {
	HTTPClient     *http.Client
	APIBaseURL     string
	GOOS           string
	GOARCH         string
	RequestTimeout time.Duration
}

// NewDiscoverer returns a release discoverer using client.
func NewDiscoverer(client *http.Client) *Discoverer {
	return &Discoverer{HTTPClient: client}
}

// Latest returns the latest stable release and the exact checksum and runtime
// archive assets defined by Tao's GoReleaser configuration.
func (discoverer *Discoverer) Latest(ctx context.Context) (Release, error) {
	if discoverer == nil {
		return Release{}, errors.New("discover latest Tao release: nil discoverer")
	}

	goos := discoverer.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := discoverer.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	if err := validateTarget(goos, goarch); err != nil {
		return Release{}, err
	}

	baseURL := strings.TrimRight(discoverer.APIBaseURL, "/")
	if baseURL == "" {
		baseURL = defaultAPIBaseURL
	}
	requestContext, cancel := discoverer.requestContext(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, baseURL+latestReleasePath, nil)
	if err != nil {
		return Release{}, fmt.Errorf("discover latest Tao release: create request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "tao-self-update")

	client := discoverer.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("discover latest Tao release: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Release{}, fmt.Errorf("discover latest Tao release: GitHub returned %s", response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseResponseBytes+1))
	if err != nil {
		return Release{}, fmt.Errorf("discover latest Tao release: read response: %w", err)
	}
	if len(body) > maxReleaseResponseBytes {
		return Release{}, fmt.Errorf("discover latest Tao release: response exceeds %d bytes", maxReleaseResponseBytes)
	}

	var metadata releaseMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return Release{}, fmt.Errorf("discover latest Tao release: decode response: %w", err)
	}
	return selectRelease(metadata, goos, goarch)
}

func (discoverer *Discoverer) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := discoverer.RequestTimeout
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

type releaseMetadata struct {
	TagName    string          `json:"tag_name"`
	Draft      bool            `json:"draft"`
	Prerelease bool            `json:"prerelease"`
	Assets     []assetMetadata `json:"assets"`
}

type assetMetadata struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func validateTarget(goos, goarch string) error {
	if goos != "darwin" && goos != "linux" {
		return fmt.Errorf("self-update is unsupported on %s/%s: supported operating systems are darwin and linux", goos, goarch)
	}
	if goarch != "amd64" && goarch != "arm64" {
		return fmt.Errorf("self-update is unsupported on %s/%s: supported architectures are amd64 and arm64", goos, goarch)
	}
	return nil
}

func selectRelease(metadata releaseMetadata, goos, goarch string) (Release, error) {
	if metadata.Draft {
		return Release{}, errors.New("discover latest Tao release: latest release is a draft")
	}
	if metadata.Prerelease {
		return Release{}, errors.New("discover latest Tao release: latest release is a prerelease")
	}
	version, err := parseStableVersion(metadata.TagName)
	if err != nil {
		return Release{}, fmt.Errorf("discover latest Tao release: invalid release tag: %w", err)
	}

	archiveName := fmt.Sprintf("tao_%d.%d.%d_%s_%s.tar.gz", version.major, version.minor, version.patch, goos, goarch)
	checksums, err := selectAsset(metadata.Assets, "checksums.txt")
	if err != nil {
		return Release{}, err
	}
	archive, err := selectAsset(metadata.Assets, archiveName)
	if err != nil {
		return Release{}, err
	}

	return Release{
		Tag:       metadata.TagName,
		Checksums: checksums,
		Archive:   archive,
	}, nil
}

func selectAsset(assets []assetMetadata, name string) (Asset, error) {
	var selected Asset
	matches := 0
	for _, candidate := range assets {
		if candidate.Name != name {
			continue
		}
		matches++
		selected = Asset{Name: candidate.Name, URL: candidate.BrowserDownloadURL}
	}

	if matches == 0 {
		return Asset{}, fmt.Errorf("discover latest Tao release: required asset %q is missing", name)
	}
	if matches > 1 {
		return Asset{}, fmt.Errorf("discover latest Tao release: required asset %q is duplicated", name)
	}
	parsedURL, err := url.Parse(selected.URL)
	if err != nil || !parsedURL.IsAbs() || (parsedURL.Scheme != "https" && parsedURL.Scheme != "http") || parsedURL.Host == "" {
		return Asset{}, fmt.Errorf("discover latest Tao release: asset %q has an invalid download URL", name)
	}
	return selected, nil
}
