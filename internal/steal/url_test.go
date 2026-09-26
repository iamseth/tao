package steal

import "testing"

func TestValidateSourceURL(t *testing.T) {
	for _, tt := range []struct{ raw, host, path string }{
		{"https://GitHub.com/Owner/Repo.git", "GitHub.com", "Owner/Repo"},
		{"ssh://git@example.com:2222/team/repo.git", "example.com", "team/repo"},
		{"git@example.com:team/repo.git", "example.com", "team/repo"},
		{"https://example.com/repo", "example.com", "repo"},
		{"git@[::1]:team/repo.git", "::1", "team/repo"},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			s, err := ValidateSourceURL(tt.raw)
			if err != nil || s != (Source{URL: tt.raw, Host: tt.host, Path: tt.path}) {
				t.Fatalf("source = %+v, error = %v", s, err)
			}
		})
	}
	for _, raw := range []string{"", "-repo", "file:///tmp/repo", "ext::command", "http://example.com/repo", "/tmp/repo", "./repo", "../repo", "repo", "~/repo", "https:///repo", "ssh:///repo", "git@:repo", "git@host:", "host:repo", "https://host", "https://host/", "ssh://host/", "https://host/re po", "git@host:repo\n", "https://host/rep\u00a0o", "https://host/repo?x=y", "https://host/repo#fragment", "user@-host:repo", "git@host:/", "https://host/.git"} {
		t.Run(raw, func(t *testing.T) {
			if s, err := ValidateSourceURL(raw); err == nil {
				t.Fatalf("accepted %q: %+v", raw, s)
			}
		})
	}
}
