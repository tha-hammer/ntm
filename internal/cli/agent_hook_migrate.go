package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/claudeconfig"
	"github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
)

// migrationDefaultFolders are scanned, under the home folder, when no
// folders are named.
var migrationDefaultFolders = []string{"Dev", "ntm_Dev"}

func newAgentHookMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate [folders...]",
		Short: "Move Claude settings from the old Agent Mail hook scripts to ntm agent-hook stop",
		Long: `Finds .claude/settings.json and .claude/settings.local.json files that register
the old Agent Mail hook scripts, and shows the change for each:
  check_critical_on_stop.sh  replaced by ~/<path to ntm> agent-hook stop
  check_mail_on_close.sh     removed (turn-end reminders cover new mail)
  ensure_window_id.sh        left alone (the breadcrumb lookup needs it)

With no folders named, it scans ~/Dev and ~/ntm_Dev. Nothing is written.
It never commits and never deletes a script.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find your home folder: %w", err)
			}
			roots, err := migrationRoots(args, home)
			if err != nil {
				return err
			}
			report := buildMigrationReport(roots, home, resilience.CurrentExecutablePath)
			out := cmd.OutOrStdout()
			if mailJSONEnabled(cmd) {
				if err := encodeJSONResult(out, report); err != nil {
					return err
				}
			} else {
				renderMigrationReport(out, report)
			}
			if len(report.Failures) > 0 {
				return fmt.Errorf("%d of the files or repos couldn't be migrated", len(report.Failures))
			}
			return nil
		},
	}
}

// migrationRoots is the folders to scan: the named ones, each of which must
// exist, or else ~/Dev and ~/ntm_Dev where they exist.
func migrationRoots(named []string, home string) ([]string, error) {
	if len(named) == 0 {
		var roots []string
		for _, dir := range migrationDefaultFolders {
			path := filepath.Join(home, dir)
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				roots = append(roots, path)
			}
		}
		return roots, nil
	}
	roots := make([]string, 0, len(named))
	for _, dir := range named {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("folder %s: %w", dir, err)
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("folder %s doesn't exist", abs)
		}
		roots = append(roots, abs)
	}
	return roots, nil
}

// migrationFile is one settings file whose migration changes something.
type migrationFile struct {
	Path         string                       `json:"path"`
	Repo         string                       `json:"repo"`
	CommitStatus string                       `json:"commit_status"`
	SharedWith   string                       `json:"shared_with,omitempty"` // main repo, for a worktree's copy
	Changes      []claudeconfig.PlannedChange `json:"changes"`
}

// migrationFailure is a file or repo that couldn't be planned or written.
type migrationFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// migrationTotals counts files, the repos you commit in (a worktree's shared
// copy counts under its main repo), shared copies, failures, and folders the
// scan couldn't read.
type migrationTotals struct {
	Files      int `json:"files"`
	Repos      int `json:"repos"`
	Shared     int `json:"shared"`
	Failed     int `json:"failed"`
	Unreadable int `json:"unreadable_folders"`
}

// migrationReport is what ntm agent-hook migrate found and did.
type migrationReport struct {
	Success  bool               `json:"success"`
	Applied  bool               `json:"applied"`
	Command  string             `json:"command,omitempty"`
	Files    []migrationFile    `json:"files"`
	Failures []migrationFailure `json:"failures"`
	Totals   migrationTotals    `json:"totals"`
}

// buildMigrationReport scans the roots and plans each settings file that
// names an old script. Files that name one but can't be read or parsed, and
// repos that would need a committed command while ntm is outside home, are
// failures; the rest go on. Files naming no old script, and folders the scan
// isn't allowed to read, are skipped.
func buildMigrationReport(roots []string, home string, ntmPath func() (string, error)) migrationReport {
	report := migrationReport{Files: []migrationFile{}, Failures: []migrationFailure{}}
	newCommand, commandErr := "", error(nil)
	if path, err := ntmPath(); err != nil {
		commandErr = err
	} else {
		newCommand, commandErr = claudeconfig.CommittedStopHookCommand(path, home)
	}
	report.Command = newCommand

	paths, unreadable, walkErrs := claudeconfig.FindHookSettings(roots)
	for _, err := range walkErrs {
		report.Failures = append(report.Failures, migrationFailure{Path: walkErrPath(err), Error: err.Error()})
	}
	binaryFailed := map[string]bool{}
	for _, path := range paths {
		// #nosec G304 -- a settings file the scan found.
		raw, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrPermission) {
			unreadable++
			continue
		}
		if err != nil {
			report.Failures = append(report.Failures, migrationFailure{Path: path, Error: err.Error()})
			continue
		}
		if !claudeconfig.MentionsOldHookScript(raw) {
			continue
		}
		settings, err := claudeconfig.ParseHookSettings(path, raw)
		if err != nil {
			report.Failures = append(report.Failures, migrationFailure{Path: path, Error: err.Error()})
			continue
		}
		plan, err := claudeconfig.PlanHookMigration(settings, newCommand)
		if err != nil {
			report.Failures = append(report.Failures, migrationFailure{Path: path, Error: err.Error()})
			continue
		}
		if !plan.HasChanges() {
			continue
		}
		repo := migrationRepo(path)
		if commandErr != nil {
			if !binaryFailed[repo] {
				binaryFailed[repo] = true
				report.Failures = append(report.Failures, migrationFailure{Path: repo, Error: commandErr.Error()})
			}
			continue
		}
		status, sharedWith := commitStatus(path, repo)
		report.Files = append(report.Files, migrationFile{Path: path, Repo: repo, CommitStatus: status, SharedWith: sharedWith, Changes: plan.Changes})
	}

	sort.SliceStable(report.Files, func(i, j int) bool { return report.Files[i].Repo < report.Files[j].Repo })
	repos := map[string]bool{}
	shared := 0
	for _, f := range report.Files {
		if f.SharedWith != "" {
			shared++
			repos[f.SharedWith] = true
		} else {
			repos[f.Repo] = true
		}
	}
	report.Totals = migrationTotals{Files: len(report.Files), Repos: len(repos), Shared: shared, Failed: len(report.Failures), Unreadable: unreadable}
	report.Success = len(report.Failures) == 0
	return report
}

// walkErrPath is the path a scan error names, when it names one.
func walkErrPath(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Path
	}
	return ""
}

// migrationRepo is the git repo holding a settings file, or the folder
// holding its .claude folder when it isn't in a repo.
func migrationRepo(path string) string {
	dir := filepath.Dir(filepath.Dir(path))
	if root, err := git.FindProjectRoot(dir); err == nil && root != "" {
		return root
	}
	return dir
}

// commitStatus says whether and where you commit a migrated file. A linked
// worktree checks out its main repo's committed file, so it's committed
// once, there; sharedWith names that main repo.
func commitStatus(path, repo string) (status, sharedWith string) {
	if filepath.Base(path) == "settings.local.json" {
		return "nothing to commit (local file)", ""
	}
	main, linked, err := git.MainWorktree(filepath.Dir(filepath.Dir(path)))
	switch {
	case err == nil && main == "":
		return "nothing to commit (not in a git repo)", ""
	case linked:
		return "shared with " + main + "; commit once there", main
	default:
		return "commit in " + repo, ""
	}
}

// renderMigrationReport prints the report for a person: one block per repo,
// failures, then totals.
func renderMigrationReport(w io.Writer, r migrationReport) {
	if len(r.Files) == 0 && len(r.Failures) == 0 {
		fmt.Fprintln(w, "Nothing to migrate.")
	}
	repo := ""
	for _, f := range r.Files {
		if f.Repo != repo {
			if repo != "" {
				fmt.Fprintln(w)
			}
			repo = f.Repo
			fmt.Fprintln(w, repo)
		}
		rel, err := filepath.Rel(f.Repo, f.Path)
		if err != nil {
			rel = f.Path
		}
		fmt.Fprintf(w, "  %s (%s)\n", rel, f.CommitStatus)
		for _, c := range f.Changes {
			fmt.Fprintf(w, "    %s\n", describeChange(c))
		}
	}
	if len(r.Failures) > 0 {
		if len(r.Files) > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "Failed:")
		for _, f := range r.Failures {
			fmt.Fprintf(w, "  %s: %s\n", f.Path, f.Error)
		}
	}
	if r.Totals.Unreadable > 0 {
		if len(r.Files) > 0 || len(r.Failures) > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "Skipped %s that couldn't be read.\n", plural(r.Totals.Unreadable, "folder"))
	}
	if len(r.Files) > 0 {
		fmt.Fprintln(w)
		shared := ""
		switch r.Totals.Shared {
		case 0:
		case 1:
			shared = " (1 is a shared worktree copy)"
		default:
			shared = fmt.Sprintf(" (%d are shared worktree copies)", r.Totals.Shared)
		}
		fmt.Fprintf(w, "Preview only: %s would change in %s%s. Run with --apply to write them.\n",
			plural(r.Totals.Files, "file"), plural(r.Totals.Repos, "repo"), shared)
	}
}

// describeChange is one change as a report line.
func describeChange(c claudeconfig.PlannedChange) string {
	where := c.Event
	if c.Matcher != "" {
		where += " (" + c.Matcher + ")"
	}
	switch c.Action {
	case claudeconfig.MigrationReplace:
		return fmt.Sprintf("replace %s: %s\n      with %s", where, c.Old, c.New)
	case claudeconfig.MigrationRemove:
		line := fmt.Sprintf("remove %s: %s", where, c.Old)
		if c.GroupEmptied {
			line += " (and its now empty group)"
		}
		return line
	default:
		return fmt.Sprintf("keep %s: %s (needed for the breadcrumb lookup)", where, c.Old)
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
