package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestXFHealthWhenInstalled is an end-to-end regression for issue #202: a
// healthy xf (installed, responds to --version) must pass even when the default
// ~/.xf/archive is absent or the archive lives at a custom path (XF_DB/XF_INDEX)
// and when the removed `xf stats --output json` probe would fail. Runs only
// where a real xf is installed.
func TestXFHealthWhenInstalled(t *testing.T) {
	adapter := NewXFAdapter()
	if _, installed := adapter.Detect(); !installed {
		t.Skip("xf not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() returned error: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("installed xf reported unhealthy (over-strict archive/index gating regression): %s", health.Message)
	}
}

func TestXFIndexStatusHealthy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      string
		healthy bool
	}{
		{name: "Empty", in: "", healthy: false},
		{name: "Whitespace", in: "   ", healthy: false},
		{name: "OK", in: "ok", healthy: true},
		{name: "Ready", in: "READY", healthy: true},
		{name: "Indexed", in: "indexed", healthy: true},
		{name: "UpToDate", in: "up-to-date", healthy: true},
		{name: "Missing", in: "missing", healthy: false},
		{name: "NotIndexed", in: "not indexed", healthy: false},
		{name: "Invalid", in: "invalid", healthy: false},
		{name: "Corrupt", in: "corrupt index", healthy: false},
		{name: "Error", in: "error: failed to open", healthy: false},
		{name: "UnknownConservative", in: "maybe", healthy: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := xfIndexStatusHealthy(tc.in)
			if got != tc.healthy {
				t.Fatalf("xfIndexStatusHealthy(%q) = %v, want %v", tc.in, got, tc.healthy)
			}
		})
	}
}

func TestXFIndexValidFromStats(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		stats XFStats
		want  bool
	}{
		{name: "Empty", stats: XFStats{}, want: false},
		{name: "TweetCount", stats: XFStats{TweetCount: 1}, want: true},
		{name: "LikeCount", stats: XFStats{LikeCount: 2}, want: true},
		{name: "DMCount", stats: XFStats{DMCount: 3}, want: true},
		{name: "GrokCount", stats: XFStats{GrokCount: 4}, want: true},
		{name: "DBPath", stats: XFStats{DatabasePath: "/tmp/xf.db"}, want: true},
		{name: "IndexStatusHealthy", stats: XFStats{IndexStatus: "ok"}, want: true},
		{name: "IndexStatusUnhealthy", stats: XFStats{IndexStatus: "missing"}, want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := xfIndexValid(tc.stats)
			if got != tc.want {
				t.Fatalf("xfIndexValid(%+v) = %v, want %v", tc.stats, got, tc.want)
			}
		})
	}
}

func TestXFHealthMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		ver         Version
		versionOK   bool
		archivePath string
		archiveOK   bool
		archiveErr  error
		indexValid  bool
		indexStatus string
		tweetCount  int
		statsErr    error
		wantParts   []string // substrings that must appear
		noParts     []string // substrings that must NOT appear
	}{
		{
			name:        "HealthyFull",
			ver:         Version{Major: 1, Minor: 0, Patch: 0, Raw: "xf 1.0.0"},
			versionOK:   true,
			archivePath: "/home/user/.xf/archive",
			archiveOK:   true,
			indexValid:  true,
			indexStatus: "ok",
			tweetCount:  15000,
			wantParts:   []string{"xf 1.0.0", "version_ok=true", "archive=/home/user/.xf/archive", "archive_ok=true", "index_valid=true", `index_status="ok"`, "tweet_count=15000"},
			noParts:     []string{"stats_err"},
		},
		{
			name:        "ArchiveMissingWithError",
			ver:         Version{Major: 0, Minor: 2, Patch: 1, Raw: "xf 0.2.1"},
			versionOK:   true,
			archivePath: "/tmp/missing",
			archiveOK:   false,
			archiveErr:  fmt.Errorf("no such file or directory"),
			indexValid:  false,
			wantParts:   []string{"archive_ok=false(no such file or directory)", "index_valid=false"},
			noParts:     []string{"tweet_count", "index_status"},
		},
		{
			name:        "ArchiveNotOKNoError",
			ver:         Version{Raw: "xf 0.1.0"},
			versionOK:   true,
			archivePath: "/tmp/not-a-dir",
			archiveOK:   false,
			indexValid:  false,
			wantParts:   []string{"archive_ok=false"},
			noParts:     []string{"archive_ok=false("},
		},
		{
			name:        "VersionFallbackToString",
			ver:         Version{Major: 2, Minor: 3, Patch: 4},
			versionOK:   true,
			archivePath: "/a",
			archiveOK:   true,
			indexValid:  true,
			wantParts:   []string{"xf 2.3.4"},
		},
		{
			name:        "VersionNotOK",
			ver:         Version{Raw: "xf 0.0.1"},
			versionOK:   false,
			archivePath: "/a",
			archiveOK:   true,
			indexValid:  true,
			wantParts:   []string{"version_ok=false"},
		},
		{
			name:        "StatsError",
			ver:         Version{Raw: "xf 1.0.0"},
			versionOK:   true,
			archivePath: "/a",
			archiveOK:   true,
			indexValid:  false,
			statsErr:    fmt.Errorf("connection refused"),
			wantParts:   []string{`stats_err="connection refused"`},
		},
		{
			name:        "ZeroTweetCountOmitted",
			ver:         Version{Raw: "xf 1.0.0"},
			versionOK:   true,
			archivePath: "/a",
			archiveOK:   true,
			indexValid:  true,
			tweetCount:  0,
			noParts:     []string{"tweet_count"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := xfHealthMessage(tc.ver, tc.versionOK, tc.archivePath, tc.archiveOK, tc.archiveErr, tc.indexValid, tc.indexStatus, tc.tweetCount, tc.statsErr)
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("xfHealthMessage() = %q, missing %q", got, want)
				}
			}
			for _, no := range tc.noParts {
				if strings.Contains(got, no) {
					t.Errorf("xfHealthMessage() = %q, should not contain %q", got, no)
				}
			}
		})
	}
}
