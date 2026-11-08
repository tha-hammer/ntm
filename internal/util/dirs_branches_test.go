package util

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// NTMDir — 0% → 100%
// ---------------------------------------------------------------------------

func TestNTMDir(t *testing.T) {
	t.Parallel()

	dir, err := NTMDir()
	if err != nil {
		t.Fatalf("NTMDir() error: %v", err)
	}

	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".ntm")
	if dir != want {
		t.Errorf("NTMDir() = %q, want %q", dir, want)
	}
}

// ---------------------------------------------------------------------------
// EnsureDir — 0% → 100%
// ---------------------------------------------------------------------------

func TestEnsureDir(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "a", "b", "c")

	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir(%q) error: %v", dir, err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat after EnsureDir: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory, got file")
	}

	// Idempotent call
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir (second call) error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FindGitRoot — 0% → 100%
// ---------------------------------------------------------------------------

func TestFindGitRoot(t *testing.T) {
	t.Parallel()

	// Create a temp git repo
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "a", "b")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	// Initialize git in tmp
	cmd := exec.Command("git", "init", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init failed: %v: %s", err, out)
	}

	root, err := FindGitRoot(sub)
	if err != nil {
		t.Fatalf("FindGitRoot(%q) error: %v", sub, err)
	}
	// Resolve symlinks for comparison (t.TempDir may use /tmp which symlinks)
	wantReal, _ := filepath.EvalSymlinks(tmp)
	gotReal, _ := filepath.EvalSymlinks(root)
	if gotReal != wantReal {
		t.Errorf("FindGitRoot(%q) = %q, want %q", sub, gotReal, wantReal)
	}
}

func TestFindGitRoot_NotARepo(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	_, err := FindGitRoot(tmp)
	if err == nil {
		t.Error("expected error for non-git directory")
	}
}

func TestFindProjectConfigRoot(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "repo")
	subDir := filepath.Join(projectDir, "internal", "robot")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := FindProjectConfigRoot(subDir)
	if err != nil {
		t.Fatalf("FindProjectConfigRoot(%q) error: %v", subDir, err)
	}
	if root != projectDir {
		t.Errorf("FindProjectConfigRoot(%q) = %q, want %q", subDir, root, projectDir)
	}
}

func TestFindBeadsRoot(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "repo")
	subDir := filepath.Join(projectDir, "internal", "robot")
	if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := FindBeadsRoot(subDir)
	if err != nil {
		t.Fatalf("FindBeadsRoot(%q) error: %v", subDir, err)
	}
	if root != projectDir {
		t.Errorf("FindBeadsRoot(%q) = %q, want %q", subDir, root, projectDir)
	}
}

func TestResolveProjectDir_PrefersProjectConfig(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "repo")
	subDir := filepath.Join(projectDir, "pkg")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ResolveProjectDir(subDir)
	if got != projectDir {
		t.Errorf("ResolveProjectDir(%q) = %q, want %q", subDir, got, projectDir)
	}
}

func TestResolveProjectDir_PrefersBeadsRoot(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "repo")
	subDir := filepath.Join(projectDir, "pkg", "sub")
	if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got := ResolveProjectDir(subDir)
	if got != projectDir {
		t.Errorf("ResolveProjectDir(%q) = %q, want %q", subDir, got, projectDir)
	}
}

func TestResolveProjectDir_FallsBackToGitRoot(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	subDir := filepath.Join(tmp, "a", "b")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init failed: %v: %s", err, out)
	}

	got := ResolveProjectDir(subDir)
	wantReal, _ := filepath.EvalSymlinks(tmp)
	gotReal, _ := filepath.EvalSymlinks(got)
	if gotReal != wantReal {
		t.Errorf("ResolveProjectDir(%q) = %q, want %q", subDir, gotReal, wantReal)
	}
}

// TestResolveProjectDir_NeverPicksHomeFolder covers bd-nftwx: a home folder
// holding the global beads store or a user .ntm/config.toml isn't a project,
// so a folder under it resolves to its own git root, or to itself.
func TestResolveProjectDir_NeverPicksHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, marker := range []string{".beads", ".ntm"} {
		if err := os.MkdirAll(filepath.Join(home, marker), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".ntm", "config.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(home, "ntm_Dev", "addtest")
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	plain := filepath.Join(home, "scratch", "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ start, want string }{
		{repo, repo},
		{filepath.Join(repo, "pkg"), repo},
		{plain, plain},
	} {
		gotReal, _ := filepath.EvalSymlinks(ResolveProjectDir(tc.start))
		wantReal, _ := filepath.EvalSymlinks(tc.want)
		if gotReal != wantReal {
			t.Errorf("ResolveProjectDir(%q) = %q, want %q", tc.start, gotReal, wantReal)
		}
		if got := BestProjectDir(tc.start); got == home {
			t.Errorf("BestProjectDir(%q) picked the home folder", tc.start)
		}
	}
	if root, err := FindBeadsRoot(plain); err == nil {
		t.Errorf("FindBeadsRoot(%q) = %q, want not found", plain, root)
	}
	if root, err := FindProjectConfigRoot(plain); err == nil {
		t.Errorf("FindProjectConfigRoot(%q) = %q, want not found", plain, root)
	}
}

func TestBestProjectDir_PrefersCandidateWithProjectSignals(t *testing.T) {
	t.Parallel()

	staleDir := filepath.Join(t.TempDir(), "ntm")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := BestProjectDir(staleDir, projectDir)
	if got != projectDir {
		t.Errorf("BestProjectDir() = %q, want %q", got, projectDir)
	}
}

func TestBestProjectDir_PreservesOrderOnTie(t *testing.T) {
	t.Parallel()

	first := t.TempDir()
	second := t.TempDir()

	got := BestProjectDir(first, second)
	if got != first {
		t.Errorf("BestProjectDir() = %q, want %q", got, first)
	}
}
