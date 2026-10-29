package cli

import (
	"log/slog"

	"github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/output"
)

// initCreatedProjectGit makes a work directory ntm just created a git
// repository with an initial commit, so agents can use worktrees in it.
// Best effort, like `ntm quick`'s git init: a failure is reported and never
// blocks the spawn. JSON mode stays silent on stdout and logs instead.
func initCreatedProjectGit(dir string) {
	created, err := git.InitRepository(dir)
	if err != nil {
		if IsJSONOutput() {
			slog.Default().Warn("git init of new project directory failed", "dir", dir, "error", err)
			return
		}
		output.PrintWarningf("Git init failed: %v", err)
		return
	}
	if created && !IsJSONOutput() {
		output.PrintSuccess("Initialized git repository")
	}
}
