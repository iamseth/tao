package steal

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Source is a validated remote source and its campaign-name components.
type Source struct {
	URL, Host, Path string
}

var scpSource = regexp.MustCompile(`^[^/@:]+@(\[[^\]]+\]|[^/:]+):(.+)$`)
var sourceHost = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// ValidateSourceURL admits only HTTPS, SSH, and user@host:path sources.
func ValidateSourceURL(raw string) (Source, error) {
	invalid := func() (Source, error) {
		return Source{}, fmt.Errorf("source must be an HTTPS, SSH, or user@host:path remote URL")
	}
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return invalid()
	}
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "ssh") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
			return invalid()
		}
		host, path = u.Hostname(), u.Path
	} else {
		parts := scpSource.FindStringSubmatch(raw)
		if parts == nil {
			return invalid()
		}
		host, path = strings.Trim(parts[1], "[]"), parts[2]
	}
	if !sourceHost.MatchString(host) && net.ParseIP(host) == nil {
		return invalid()
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if path == "" {
		return invalid()
	}
	return Source{URL: raw, Host: host, Path: path}, nil
}
