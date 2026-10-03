package promptcapture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxPromptBytes = 512 * 1024

type Target struct {
	Dir, Role, Template, TemplateHash, Label string
}

type Meta struct {
	Agent, Model, Effort string
	StartedAt            time.Time
}

type Header struct {
	Role, Template, TemplateHash, Agent, Model, Effort, StartedAt, SHA256 string
	Bytes                                                                 int64
	Truncated                                                             bool
}

type Written struct {
	Path, Hash string
	Truncated  bool
}

type Entry struct {
	Path, Name string
	Header     Header
}

func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func Dir(parent string) string { return filepath.Join(parent, "prompts") }

func namePart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func Write(target Target, meta Meta, prompt string) (Written, error) {
	hash := Hash(prompt)
	body := prompt
	if len(body) > MaxPromptBytes {
		end := MaxPromptBytes
		for end > 0 && !utf8.RuneStart(body[end]) {
			end--
		}
		body = body[:end]
	}
	truncated := len(body) < len(prompt)
	templateHash := target.TemplateHash
	if templateHash == "" {
		templateHash = "unknown"
	}
	// Reject line breaks rather than allowing metadata to forge the body boundary.
	for _, value := range []string{target.Role, target.Template, templateHash, meta.Agent, meta.Model, meta.Effort} {
		if strings.ContainsAny(value, "\r\n") {
			return Written{}, fmt.Errorf("prompt capture metadata contains a line break")
		}
	}
	header := fmt.Sprintf("role: %s\ntemplate: %s\ntemplate_hash: %s\nagent: %s\nmodel: %s\neffort: %s\nstarted_at: %s\nsha256: %s\nbytes: %d\ntruncated: %t\n\n", target.Role, target.Template, templateHash, meta.Agent, meta.Model, meta.Effort, meta.StartedAt.UTC().Format(time.RFC3339), hash, len(prompt), truncated)
	if err := os.MkdirAll(target.Dir, 0o700); err != nil {
		return Written{}, err
	}
	role := namePart(target.Role)
	if role == "" {
		role = "unknown"
	}
	stem := meta.StartedAt.UTC().Format("20060102T150405Z") + "-" + role
	if label := namePart(target.Label); label != "" {
		stem += "-" + label
	}
	stem += "-" + hash[:8]
	for n := 1; ; n++ {
		name := stem
		if n > 1 {
			name += "-" + strconv.Itoa(n)
		}
		path := filepath.Join(target.Dir, name+".md")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- caller-owned capture directory and sanitized basename; exclusive creation prevents overwrite.
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return Written{}, err
		}
		_, writeErr := f.WriteString(header + body)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			if writeErr != nil {
				return Written{}, writeErr
			}
			return Written{}, closeErr
		}
		return Written{Path: path, Hash: hash, Truncated: truncated}, nil
	}
}

func List(dir string) ([]Entry, error) {
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []Entry
	// ReadDir returns entries sorted by filename.
	for _, file := range files {
		if !file.Type().IsRegular() || filepath.Ext(file.Name()) != ".md" {
			continue
		}
		path := filepath.Join(dir, file.Name())
		h, _, err := Read(path)
		if err != nil {
			continue
		}
		entries = append(entries, Entry{Path: path, Name: file.Name(), Header: h})
	}
	return entries, nil
}

func Parse(data []byte) (Header, string, error) {
	head, body, ok := strings.Cut(string(data), "\n\n")
	if !ok {
		return Header{}, "", fmt.Errorf("prompt capture missing header boundary")
	}
	fields := make(map[string]string)
	for _, line := range strings.Split(head, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Header{}, "", fmt.Errorf("invalid prompt capture header line")
		}
		if _, exists := fields[key]; exists {
			return Header{}, "", fmt.Errorf("duplicate prompt capture field %q", key)
		}
		fields[key] = strings.TrimPrefix(value, " ")
	}
	for _, key := range []string{"role", "template", "template_hash", "agent", "model", "effort", "started_at", "sha256", "bytes", "truncated"} {
		if _, ok := fields[key]; !ok {
			return Header{}, "", fmt.Errorf("missing prompt capture field %q", key)
		}
	}
	size, err := strconv.ParseInt(fields["bytes"], 10, 64)
	if err != nil || size < 0 {
		return Header{}, "", fmt.Errorf("invalid prompt capture byte count")
	}
	if fields["truncated"] != "true" && fields["truncated"] != "false" {
		return Header{}, "", fmt.Errorf("invalid prompt capture truncation flag")
	}
	if _, err := time.Parse(time.RFC3339, fields["started_at"]); err != nil {
		return Header{}, "", fmt.Errorf("invalid prompt capture timestamp: %w", err)
	}
	hash := fields["sha256"]
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(hash) != hash {
		return Header{}, "", fmt.Errorf("invalid prompt capture hash")
	}
	return Header{Role: fields["role"], Template: fields["template"], TemplateHash: fields["template_hash"], Agent: fields["agent"], Model: fields["model"], Effort: fields["effort"], StartedAt: fields["started_at"], SHA256: hash, Bytes: size, Truncated: fields["truncated"] == "true"}, body, nil
}

func Read(path string) (Header, string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- explicit local capture path selected by the caller.
	if err != nil {
		return Header{}, "", err
	}
	return Parse(data)
}
