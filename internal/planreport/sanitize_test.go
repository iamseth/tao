package planreport

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeRedactsSensitiveValuesWithoutEchoingThem(t *testing.T) {
	secret := "AKIA1234567890ABCDEF"
	email := "alice.smith@example.com"
	phone := "+1 (415) 555-0123"
	path := "/home/alice/private/plan.md"
	url := "https://alice:hunter2@example.com/repo" //nolint:gosec // deliberate credential-shaped sanitizer fixture
	input := "Owner " + email + " called " + phone + "; key " + secret + "; file " + path + "; remote " + url

	s := NewSanitizer(0)
	got := s.Sanitize("context", input).text
	for _, tainted := range []string{secret, email, phone, url, "hunter2"} {
		if strings.Contains(got, tainted) {
			t.Fatalf("safe text retained tainted value %q in %q", tainted, got)
		}
	}
	if !strings.Contains(got, path) {
		t.Fatalf("safe text removed coworker-accessible path %q from %q", path, got)
	}
	for _, placeholder := range []string{"email redacted", "phone redacted", "credential redacted", "credential URL redacted"} {
		if !strings.Contains(got, placeholder) {
			t.Errorf("safe text missing typed placeholder %q: %q", placeholder, got)
		}
	}
	if err := ValidateDocument([]byte(got)); err != nil {
		t.Fatalf("sanitized text failed final scan: %v", err)
	}
}

func TestSanitizePreservesOrdinaryURLsAndFilesystemPaths(t *testing.T) {
	values := []string{
		"https://docs.example.com/project/guide",
		"ftp://artifacts.example.com/releases/latest",
		"ssh://git.example.com/repository",
		"file:///srv/company/plan.md",
		"//internal.example.com/private",
		"www.example.com/private",
		"portal.example.org:8443/reports?id=project",
		"/srv/company/plan.md",
		"/workspace/repo",
		"~alice/private/plan.md",
	}

	s := NewSanitizer(0)
	got := s.Sanitize("context", "Coworker resources: "+strings.Join(values, ", ")).text
	for _, value := range values {
		if !strings.Contains(got, value) {
			t.Errorf("safe text removed ordinary URL or path %q from %q", value, got)
		}
	}
	if len(s.Disclosures()) != 0 {
		t.Fatalf("ordinary URLs and paths produced disclosures: %#v", s.Disclosures())
	}
	if err := ValidateDocument([]byte(got)); err != nil {
		t.Fatalf("coworker-safe resources failed final scan: %v", err)
	}
}

func TestSanitizeRedactsCredentialBearingURLsAcrossSchemes(t *testing.T) {
	urls := []string{
		"https://alice:hunter2@example.com/repo",
		"ssh://deploy:private@git.example.com/repository",
		"//build:secret@artifacts.example.com/releases",
	}
	s := NewSanitizer(0)
	got := s.Sanitize("context", "Remotes: "+strings.Join(urls, ", ")).text
	for _, url := range urls {
		if strings.Contains(got, url) {
			t.Errorf("safe text retained credential-bearing URL %q in %q", url, got)
		}
	}
	if count := strings.Count(got, "credential URL redacted"); count != len(urls) {
		t.Fatalf("credential URL redaction count = %d, want %d: %q", count, len(urls), got)
	}
	if err := ValidateDocument([]byte(got)); err != nil {
		t.Fatalf("sanitized credential URLs failed final scan: %v", err)
	}
}

func TestSanitizeCredentials(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"github", "ghp_abcdefghijklmnopqrstuvwxyz1234567890"},
		{"slack", "xoxb-1234567890-abcdefghijkl"},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop"},
		{"bearer", "Bearer abcdefghijklmnopqrstuvwxyz"},
		{"setting", "password=correct-horse-battery-staple"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSanitizer(0)
			got := s.Sanitize("execution", "configured "+tc.value+" successfully").text
			if strings.Contains(got, tc.value) {
				t.Fatalf("credential survived: %q", got)
			}
			if !strings.Contains(got, "credential redacted") {
				t.Fatalf("missing credential placeholder: %q", got)
			}
			if err := ValidateDocument([]byte(got)); err != nil {
				t.Fatalf("sanitized credential failed final scan: %v; output %q", err, got)
			}
		})
	}
}

func TestSanitizeOmitsUnsafeStandaloneValuesAndPrivateKeys(t *testing.T) {
	inputs := []string{
		"alice@example.com",
		"-----BEGIN PRIVATE KEY-----\nvery-secret-material\n-----END PRIVATE KEY-----",
	}
	for _, input := range inputs {
		s := NewSanitizer(0)
		got := s.Sanitize("planning", input)
		if got.text != "" {
			t.Errorf("unsafe standalone value was retained as %q", got.text)
		}
		disclosures := s.Disclosures()
		if len(disclosures) != 1 || disclosures[0].Category != DisclosureOmitted || disclosures[0].Count != 1 {
			t.Errorf("unexpected disclosures: %#v", disclosures)
		}
		if strings.Contains(got.text, input) {
			t.Fatal("omitted source was retained")
		}
	}
}

func TestSanitizeNormalizesEscapesAndTruncatesUnicode(t *testing.T) {
	s := NewSanitizer(24)
	input := string([]byte{'a', 0xff, '\t', 0x01}) + " # title\n<script>*bold*</script> 世界世界世界"
	got := s.Sanitize("summary", input).text
	if !utf8.ValidString(got) {
		t.Fatal("result is not valid UTF-8")
	}
	if strings.ContainsAny(got, "\t\x01") || strings.Contains(got, "<script>") || strings.Contains(got, "*bold*") {
		t.Fatalf("active input survived normalization/escaping: %q", got)
	}
	if utf8.RuneCountInString(got) != 24 {
		t.Fatalf("rune limit not applied: %d %q", utf8.RuneCountInString(got), got)
	}
	want := []Disclosure{
		{Section: "summary", Category: DisclosureNormalized, Count: 3},
		{Section: "summary", Category: DisclosureTruncated, Count: 1},
	}
	if !reflect.DeepEqual(s.Disclosures(), want) {
		t.Fatalf("disclosures = %#v, want %#v", s.Disclosures(), want)
	}
}

func TestSanitizeFalsePositiveBoundaries(t *testing.T) {
	planID := "20260804-151535-plan-reports"
	input := "Plan " + planID + " build 123456789 uses relative/path.txt and café/path.txt and user@example without a TLD. Mention AKIA123 and issue #123."
	s := NewSanitizer(0)
	got := s.Sanitize("context", input).text
	for _, retained := range []string{planID, "123456789", "relative/path.txt", "café/path.txt", "user@example", "AKIA123"} {
		if !strings.Contains(got, retained) {
			t.Errorf("benign boundary %q was removed from %q", retained, got)
		}
	}
	if len(s.Disclosures()) != 0 {
		t.Fatalf("benign text produced disclosures: %#v", s.Disclosures())
	}
	if err := ValidateDocument([]byte(got)); err != nil {
		t.Fatalf("sanitized Tao plan ID failed final scan: %v", err)
	}
}

func TestDisclosuresAggregateAndSortWithoutValues(t *testing.T) {
	s := NewSanitizer(0)
	s.Sanitize("z-section", "mail first@example.com and second@example.com")
	s.Sanitize("a-section", "call 212-555-0199")
	s.Sanitize("z-section", "key AKIA1234567890ABCDEF")
	want := []Disclosure{
		{Section: "a-section", Category: DisclosureRedacted, Count: 1},
		{Section: "z-section", Category: DisclosureRedacted, Count: 3},
	}
	if got := s.Disclosures(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Disclosures() = %#v, want %#v", got, want)
	}
}

func TestValidateDocumentRejectsResidualProhibitedPatternsWithoutEcho(t *testing.T) {
	prohibited := []string{
		"Contact alice@example.com",
		"key AKIA1234567890ABCDEF",
		"remote https://alice:secret@example.com/x",
		"-----BEGIN PRIVATE KEY-----",
		"[click](https://example.com)",
		"<b>active HTML</b>",
		"# injected heading",
	}
	for _, document := range prohibited {
		err := ValidateDocument([]byte(document))
		if err == nil {
			t.Errorf("ValidateDocument accepted %q", document)
			continue
		}
		if strings.Contains(err.Error(), document) {
			t.Errorf("error echoed prohibited input: %v", err)
		}
	}
	allowed := "Resources: https://example.com/project, /srv/company/plan.md, ~/.config/tao, and C:\\\\work\\repo"
	if err := ValidateDocument([]byte(allowed)); err != nil {
		t.Fatalf("coworker-safe URLs and paths rejected: %v", err)
	}
}

func TestSanitizeIsDeterministic(t *testing.T) {
	input := "Email alice@example.com from /tmp/private/file and call +44 20 7946 0958"
	first := NewSanitizer(80)
	second := NewSanitizer(80)
	if got, want := first.Sanitize("summary", input).text, second.Sanitize("summary", input).text; got != want {
		t.Fatalf("outputs differ: %q != %q", got, want)
	}
	if !reflect.DeepEqual(first.Disclosures(), second.Disclosures()) {
		t.Fatalf("disclosures differ: %#v != %#v", first.Disclosures(), second.Disclosures())
	}
}

func FuzzSanitizeNeverReturnsInvalidOrActiveText(f *testing.F) {
	for _, seed := range []string{
		"plain text",
		"alice@example.com AKIA1234567890ABCDEF",
		"-----BEGIN PRIVATE KEY-----\nsecret",
		"<img src=x onerror=alert(1)> [link](https://example.com)",
		"ftp://example.com/private ssh://host/path file:///etc/passwd",
		"data:text/plain;base64,SGVsbG8= mailto:alice@example.com urn:isbn:9780140328721",
		"//internal.example.com/private www.example.com/private portal.example.org:8443/reports",
		`\\server\share\private.txt ~/.ssh/id_ed25519 ~alice/private/plan.md`,
		string([]byte{0xff, 0, 'a'}),
		"Unicode 👩🏽‍💻 café 世界",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		s := NewSanitizer(256)
		got := s.Sanitize("fuzz", input).text
		if !utf8.ValidString(got) {
			t.Fatal("invalid UTF-8")
		}
		if utf8.RuneCountInString(got) > 256 {
			t.Fatal("unbounded result")
		}
		if err := ValidateDocument([]byte(got)); err != nil {
			t.Fatalf("sanitizer emitted text rejected by final scan: %v; output %q", err, got)
		}
	})
}
