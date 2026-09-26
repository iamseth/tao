package steal

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCampaignSlugAndScratchDir(t *testing.T) {
	now := time.Date(2026, 9, 26, 23, 4, 5, 0, time.FixedZone("west", -3600))
	source, err := ValidateSourceURL("https://GitHub.COM/Some_Team/My.Repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if got := CampaignSlug(source, now); got != "steal-github-com-some-team-my-repo-2026-09-27" {
		t.Fatal(got)
	}
	if got := ScratchDir("/data", source, now); got != filepath.Join("/data", "steal", "github-com-some-team-my-repo-20260927-000405") {
		t.Fatal(got)
	}
	source.Path = strings.Repeat("A", 200)
	tag := CampaignSlug(source, now)
	if len(tag) != 80 || !strings.HasPrefix(tag, "steal-github-com-") || !strings.HasSuffix(tag, "-2026-09-27") {
		t.Fatal(tag)
	}
	if got := ScratchDir("/data", Source{Host: "..HOST..", Path: "../A__B.git"}, now); !strings.HasSuffix(got, "host-a-b-20260927-000405") {
		t.Fatal(got)
	}
}
