package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/claudeconfig"
	"github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
)

// migrationDefaultFolders are scanned, under the home folder, when no
// folders are named.
var migrationDefaultFolders = []string{"Dev", "ntm_Dev"}

// migrationCommitMessage is the message in the commit lines --apply prints.
const migrationCommitMessage = "Move Agent Mail hooks to ntm agent-hook stop"

func newAgentHookMigrateCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "migrate [folders...]",
		Short: "Move Claude settings from the old Agent Mail hook scripts to ntm agent-hook stop",
		Long: `Finds .claude/settings.json and .claude/settings.local.json files that register
the old Agent Mail hook scripts, and shows the change for each:
  check_critical_on_stop.sh  replaced by ~/<path to ntm> agent-hook stop
  check_mail_on_close.sh     removed (turn-end reminders cover new mail)
  ensure_window_id.sh        left alone (the breadcrumb lookup needs it)

With no folders named, it scans ~/Dev and ~/ntm_Dev. Without --apply nothing
is written. With --apply it edits only the changed lines of each file, skips
a worktree's shared copy (migrate and commit the main repo, then update the
worktree), and prints the git commands to commit each repo. It never commits
and never deletes a script.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find your home folder: %w", err)
			}
			roots, err := migrationRoots(args, home)
			if err != nil {
				return err
			}
			report := buildMigrationReport(roots, home, resilience.CurrentExecutablePath, apply)
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
	cmd.Flags().BoolVar(&apply, "apply", false, "write the changes (default: preview only)")
	return cmd
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
	Written      bool                         `json:"written"`
	Changes      []claudeconfig.PlannedChange `json:"changes"`
}

// migrationFailure is a file or repo that couldn't be planned or written.
type migrationFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// migrationTotals counts files, the repos you commit in (a worktree's shared
// copy counts under its main repo), shared copies, failures, and folders the
// scan couldn't read. With --apply it also counts the files written and the
// repos they're in.
type migrationTotals struct {
	Files        int `json:"files"`
	Repos        int `json:"repos"`
	Shared       int `json:"shared"`
	Failed       int `json:"failed"`
	Unreadable   int `json:"unreadable_folders"`
	Written      int `json:"written"`
	WrittenRepos int `json:"written_repos"`
}

// migrationReport is what ntm agent-hook migrate found and did. Commits are
// the git commands that commit each written repo, which ntm never runs.
type migrationReport struct {
	Success  bool               `json:"success"`
	Applied  bool               `json:"applied"`
	Command  string             `json:"command,omitempty"`
	Files    []migrationFile    `json:"files"`
	Failures []migrationFailure `json:"failures"`
	Commits  []string           `json:"commit_commands"`
	Totals   migrationTotals    `json:"totals"`
}

// buildMigrationReport scans the roots and plans each settings file that
// names an old script. Files that name one but can't be read or parsed, and
// repos that would need a committed command while ntm is outside home, are
// failures; the rest go on. Files naming no old script, and folders the scan
// isn't allowed to read, are skipped. With apply, each planned file is
// written, except a worktree's shared copy; a failed write is a failure.
func buildMigrationReport(roots []string, home string, ntmPath func() (string, error), apply bool) migrationReport {
	report := migrationReport{Applied: apply, Files: []migrationFile{}, Failures: []migrationFailure{}, Commits: []string{}}
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
		file := migrationFile{Path: path, Repo: repo, CommitStatus: status, SharedWith: sharedWith, Changes: plan.Changes}
		switch {
		case !apply:
		case sharedWith != "":
			file.CommitStatus = "skipped: migrate and commit " + sharedWith + ", then update this worktree"
		default:
			if err := claudeconfig.WriteMigratedSettings(path, raw, plan, newCommand); err != nil {
				report.Failures = append(report.Failures, migrationFailure{Path: path, Error: err.Error()})
				continue
			}
			file.Written = true
		}
		report.Files = append(report.Files, file)
	}

	sort.SliceStable(report.Files, func(i, j int) bool { return report.Files[i].Repo < report.Files[j].Repo })
	report.Totals = migrationTotals{Files: len(report.Files), Failed: len(report.Failures), Unreadable: unreadable}
	repos := map[string]bool{}
	writtenRepos := map[string]bool{}
	for _, f := range report.Files {
		if f.SharedWith != "" {
			report.Totals.Shared++
			repos[f.SharedWith] = true
		} else {
			repos[f.Repo] = true
		}
		if f.Written {
			report.Totals.Written++
			writtenRepos[f.Repo] = true
		}
	}
	report.Totals.Repos, report.Totals.WrittenRepos = len(repos), len(writtenRepos)
	report.Commits = commitCommands(report.Files)
	report.Success = len(report.Failures) == 0
	return report
}

// commitCommands is one git command line per repo with written files to
// commit, in the order the files are listed.
func commitCommands(files []migrationFile) []string {
	var repos []string
	paths := map[string][]string{}
	for _, f := range files {
		if !f.Written || !strings.HasPrefix(f.CommitStatus, "commit in ") {
			continue
		}
		rel, err := filepath.Rel(f.Repo, f.Path)
		if err != nil {
			continue
		}
		if paths[f.Repo] == nil {
			repos = append(repos, f.Repo)
		}
		paths[f.Repo] = append(paths[f.Repo], claudeconfig.QuotedPath(rel))
	}
	commands := []string{}
	for _, repo := range repos {
		quoted := claudeconfig.QuotedPath(repo)
		commands = append(commands, fmt.Sprintf("git -C %s add %s && git -C %s commit -m %q",
			quoted, strings.Join(paths[repo], " "), quoted, migrationCommitMessage))
	}
	return commands
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
		if r.Applied && f.SharedWith != "" {
			continue // skipped; its changes come from the main repo's commit
		}
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
	switch {
	case r.Applied && r.Totals.Written > 0:
		fmt.Fprintf(w, "\nChanged %s in %s. Check each diff, then commit:\n",
			plural(r.Totals.Written, "file"), plural(r.Totals.WrittenRepos, "repo"))
		for _, c := range r.Commits {
			fmt.Fprintf(w, "  %s\n", c)
		}
	case r.Applied && len(r.Files) > 0:
		fmt.Fprintln(w, "\nNo files changed.")
	case len(r.Files) > 0:
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
