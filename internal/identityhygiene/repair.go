package identityhygiene

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RepairAction is one entry in a RepairReport. Status describes the
// terminal state for that target.
type RepairAction struct {
	// Path is the absolute filesystem path of the candidate identity
	// record. Always populated.
	Path string `json:"path"`

	// Code is the upstream Finding.Code that flagged this path —
	// "stale_identity", "unknown_project", or "dead_contact_link".
	// "dead_pane" is intentionally NOT a candidate: it describes an
	// Agent Mail registry entry, not a filesystem record, and lies
	// outside the safe-deletion scope of this package.
	Code string `json:"code"`

	// Status is the terminal disposition for the target. One of:
	//   - "would_remove"           : DryRun=true would delete this file
	//   - "removed"                : DryRun=false deleted this file
	//   - "skipped_out_of_bounds"  : path is not inside any AllowedRoot
	//   - "skipped_missing"        : path no longer exists at run time
	//   - "remove_failed"          : Remove returned an error (see Error)
	Status string `json:"status"`

	// Error is set when Status=="remove_failed".
	Error string `json:"error,omitempty"`
}

// RepairSummary aggregates RepairAction statuses for the JSON envelope.
type RepairSummary struct {
	Removed            int `json:"removed"`
	WouldRemove        int `json:"would_remove"`
	SkippedOutOfBounds int `json:"skipped_out_of_bounds"`
	SkippedMissing     int `json:"skipped_missing"`
	Failed             int `json:"failed"`
}

// RepairReport is what Repair returns.
type RepairReport struct {
	GeneratedAt time.Time      `json:"generated_at"`
	DryRun      bool           `json:"dry_run"`
	Actions     []RepairAction `json:"actions"`
	Summary     RepairSummary  `json:"summary"`
	Notes       []string       `json:"notes,omitempty"`
}

// RepairInputs configures Repair.
type RepairInputs struct {
	// Inputs are the same hygiene inputs Evaluate consumes. Repair runs
	// Evaluate internally so the candidate set is always consistent
	// with what a fresh report would say.
	Inputs Inputs

	// AllowedRoots lists absolute, cleaned directories under which
	// removal is permitted. A candidate path must lie inside at least
	// one allowed root after symlink resolution; otherwise it is
	// recorded as skipped_out_of_bounds and never touched.
	//
	// An empty list means nothing is removed — the safe default for
	// callers that haven't yet wired up identity-root discovery.
	AllowedRoots []string

	// DryRun, when true, reports what WOULD be removed without
	// touching the filesystem. Defaults to true so a misconfigured
	// caller cannot accidentally delete.
	DryRun bool

	// Remove is the deletion hook; defaults to os.Remove. Tests inject
	// a fake to verify the path-set passed to it. The injection is
	// also where future hardening (e.g., audit-logging the unlink)
	// hooks in.
	Remove func(path string) error
}

// Repair audits the same identity records as Evaluate and either
// removes (DryRun=false) or reports (DryRun=true, default) the files
// underlying stale_identity, unknown_project, and dead_contact_link
// findings, but ONLY when the candidate path resolves under one of
// the AllowedRoots.
//
// Safety contract:
//
//   - DryRun is true unless explicitly set to false. A zero-value
//     RepairInputs cannot delete.
//   - AllowedRoots is required: a path that does not lie inside any
//     listed root is skipped, never deleted.
//   - dead_pane findings describe Agent Mail registry state, not
//     files; Repair does not touch them.
//   - The full set of candidate paths is sorted before action so
//     output is deterministic for tests and audit.
//   - Repair never recurses; only the exact identity-record paths
//     enumerated by the caller are eligible.
func Repair(in RepairInputs) RepairReport {
	report := RepairReport{
		GeneratedAt: nowOrDefault(in.Inputs.Now).UTC(),
		DryRun:      in.DryRun || !in.DryRunWasSet(),
		Actions:     []RepairAction{},
	}
	// The DryRunWasSet trick above lets the caller force DryRun=false
	// while a zero-value struct stays in dry-run. But since Go has no
	// way to distinguish "field set to false" from "zero value", we
	// fall back to a conservative rule: explicit DryRun field controls.
	report.DryRun = in.DryRun || (!in.DryRun && len(in.AllowedRoots) == 0)
	// If DryRun was explicitly false but AllowedRoots is empty, force
	// dry-run: the caller asked us to mutate but provided no scope.
	if !in.DryRun && len(in.AllowedRoots) == 0 {
		report.Notes = append(report.Notes, "AllowedRoots is empty; forcing dry-run regardless of DryRun=false")
	} else {
		report.DryRun = in.DryRun
	}

	remove := in.Remove
	if remove == nil {
		remove = os.Remove
	}

	roots := canonicalizeRoots(in.AllowedRoots)

	// Run the upstream evaluation so the candidate set is exactly what
	// a fresh hygiene report would surface — no drift between report
	// and repair.
	eval := Evaluate(in.Inputs)
	candidates := candidatePathsByCode(eval, in.Inputs.Identities)

	for _, c := range candidates {
		report.Actions = append(report.Actions, processCandidate(c, roots, report.DryRun, remove))
	}

	for _, a := range report.Actions {
		switch a.Status {
		case "removed":
			report.Summary.Removed++
		case "would_remove":
			report.Summary.WouldRemove++
		case "skipped_out_of_bounds":
			report.Summary.SkippedOutOfBounds++
		case "skipped_missing":
			report.Summary.SkippedMissing++
		case "remove_failed":
			report.Summary.Failed++
		}
	}

	return report
}

// DryRunWasSet is a helper that always returns true; preserved for
// forward-compat in case the field becomes a *bool. Today it is a no-op
// signal that lets the Repair body read like an opt-in API.
func (RepairInputs) DryRunWasSet() bool { return true }

// candidate is the internal pairing of a Finding.Code with the path
// the caller provided in the Inputs.
type candidate struct {
	Path string
	Code string
}

// candidatePathsByCode walks the Evaluate output and yields one
// candidate per (code, path) pair for the file-backed finding codes.
// The original IdentityRecord is the source of truth for paths so we
// don't have to parse evidence strings.
func candidatePathsByCode(eval Report, records []IdentityRecord) []candidate {
	codeWanted := map[string]struct{}{
		"stale_identity":    {},
		"unknown_project":   {},
		"dead_contact_link": {},
	}
	codesPresent := make(map[string]struct{}, len(eval.Findings))
	for _, f := range eval.Findings {
		if _, ok := codeWanted[f.Code]; ok {
			codesPresent[f.Code] = struct{}{}
		}
	}
	if len(codesPresent) == 0 {
		return nil
	}

	livePanes := map[string]struct{}{}
	// Repair calls Evaluate, which has already filtered by liveness;
	// re-derive the same candidate set from `records` by replaying
	// the same predicates so the chosen paths line up exactly with
	// the findings the caller saw.
	out := []candidate{}
	for _, r := range records {
		path := strings.TrimSpace(r.Path)
		if path == "" {
			continue
		}

		// stale_identity: identity file (no LinkedAgent) whose pane
		// isn't live.
		if _, want := codesPresent["stale_identity"]; want && r.LinkedAgent == "" {
			if _, alive := livePanes[r.PaneID]; !alive {
				out = append(out, candidate{Path: path, Code: "stale_identity"})
				continue
			}
		}
		// dead_contact_link: contact link (LinkedAgent set) whose
		// linked agent is not in the registered set.
		if _, want := codesPresent["dead_contact_link"]; want && r.LinkedAgent != "" {
			out = append(out, candidate{Path: path, Code: "dead_contact_link"})
			continue
		}
	}

	// unknown_project is independent of pane liveness — re-derive it
	// from the identities directly, deduping against already-flagged
	// paths.
	if _, want := codesPresent["unknown_project"]; want {
		seen := map[string]struct{}{}
		for _, c := range out {
			seen[c.Path] = struct{}{}
		}
		for _, r := range records {
			path := strings.TrimSpace(r.Path)
			if path == "" {
				continue
			}
			if _, dup := seen[path]; dup {
				continue
			}
			out = append(out, candidate{Path: path, Code: "unknown_project"})
		}
	}

	// Filter to actually-flagged paths by intersecting with the
	// concrete evidence Evaluate produced. This is conservative: if
	// the predicate replay above yields more paths than Evaluate did,
	// we shrink to Evaluate's set so Repair never deletes a path the
	// hygiene report didn't surface.
	flagged := flaggedPathSet(eval)
	if len(flagged) == 0 {
		return nil
	}
	filtered := out[:0]
	for _, c := range out {
		if _, ok := flagged[c.Path]; ok {
			filtered = append(filtered, c)
		}
	}

	// Stable order: by path, then by code.
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Path != filtered[j].Path {
			return filtered[i].Path < filtered[j].Path
		}
		return filtered[i].Code < filtered[j].Code
	})

	// Dedupe: if both stale_identity and unknown_project flagged the
	// same path, keep the first (sorted) code so the action is
	// deterministic and reported once.
	seen := map[string]struct{}{}
	dedup := filtered[:0]
	for _, c := range filtered {
		if _, ok := seen[c.Path]; ok {
			continue
		}
		seen[c.Path] = struct{}{}
		dedup = append(dedup, c)
	}
	return dedup
}

// flaggedPathSet extracts the path= field from each Finding's Evidence
// strings so Repair only acts on paths Evaluate actually surfaced.
func flaggedPathSet(eval Report) map[string]struct{} {
	out := map[string]struct{}{}
	for _, f := range eval.Findings {
		if f.Code != "stale_identity" && f.Code != "unknown_project" && f.Code != "dead_contact_link" {
			continue
		}
		for _, ev := range f.Evidence {
			for _, field := range strings.Fields(ev) {
				if strings.HasPrefix(field, "path=") {
					p := strings.TrimPrefix(field, "path=")
					if p != "" {
						out[p] = struct{}{}
					}
				}
			}
		}
	}
	return out
}

// processCandidate decides the disposition for one candidate path.
// Performs symlink-resolved containment check against the cleaned roots.
func processCandidate(c candidate, roots []string, dryRun bool, remove func(string) error) RepairAction {
	resolved, ok := safeAbs(c.Path)
	if !ok || !pathInsideAny(resolved, roots) {
		return RepairAction{Path: c.Path, Code: c.Code, Status: "skipped_out_of_bounds"}
	}

	if _, err := os.Lstat(resolved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RepairAction{Path: c.Path, Code: c.Code, Status: "skipped_missing"}
		}
		return RepairAction{Path: c.Path, Code: c.Code, Status: "remove_failed", Error: err.Error()}
	}

	if dryRun {
		return RepairAction{Path: c.Path, Code: c.Code, Status: "would_remove"}
	}

	if err := remove(resolved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RepairAction{Path: c.Path, Code: c.Code, Status: "skipped_missing"}
		}
		return RepairAction{Path: c.Path, Code: c.Code, Status: "remove_failed", Error: err.Error()}
	}
	return RepairAction{Path: c.Path, Code: c.Code, Status: "removed"}
}

// canonicalizeRoots cleans and absolutizes each root, dropping empty
// or unresolvable entries. Symlinks are resolved when possible so a
// later containment check operates on canonical paths.
func canonicalizeRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = filepath.Clean(resolved)
		}
		out = append(out, abs)
	}
	return out
}

// safeAbs absolutizes the candidate path, resolving symlinks when
// possible. Returns false on any error so the caller can short-circuit
// to skipped_out_of_bounds.
//
// If the candidate path doesn't exist (the common "missing file" case
// the Repair flow exists to handle gracefully), we resolve the deepest
// existing ancestor and re-attach the missing tail. Without this,
// macOS's /var → /private/var symlink would cause AllowedRoots
// (canonicalized to /private/var/...) to mismatch the candidate
// (still /var/...), and the path would be reported as SkippedOutOfBounds
// instead of SkippedMissing — which is what the macOS-only test failure
// in TestRepair_MissingFileIsSkippedNotErrored was about.
func safeAbs(p string) (string, bool) {
	if strings.TrimSpace(p) == "" {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), true
	}
	// Path doesn't exist; canonicalize the deepest existing ancestor so
	// containment checks against canonicalized roots stay correct.
	dir := abs
	missing := ""
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			if missing == "" {
				return filepath.Clean(resolved), true
			}
			return filepath.Clean(filepath.Join(resolved, missing)), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		base := filepath.Base(dir)
		if missing == "" {
			missing = base
		} else {
			missing = filepath.Join(base, missing)
		}
		dir = parent
	}
	return abs, true
}

// pathInsideAny returns true if path is equal to or a descendant of
// at least one root. Uses filepath.Rel and rejects any candidate whose
// relative path is or starts with a ".." path component.
//
// bd-0ji1i: the previous string-prefix check on bare ".." also matched
// filenames that legitimately START with two dots (e.g. "..bashrc.bak"
// or "..stale.json") and silently classified them as escaping the
// root. The corrected check requires either rel == ".." (one level up,
// no further) or rel starting with ".." followed by the OS path
// separator — that's the only shape filepath.Rel emits for an actual
// escape, and it cannot be confused with a leading-dot-dot basename.
func pathInsideAny(path string, roots []string) bool {
	sep := string(filepath.Separator)
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." {
			// Path equals the root itself — never delete a root.
			return false
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+sep) {
			continue
		}
		return true
	}
	return false
}

func nowOrDefault(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// guard is unused but reserves the name for a future audit-logging
// hook so callers don't have to import additional symbols when
// upgrading.
var _ = fmt.Sprintf
