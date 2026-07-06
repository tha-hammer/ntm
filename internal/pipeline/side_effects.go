package pipeline

import (
	"fmt"
	"sort"
	"strings"
)

const (
	sideEffectPhaseMain         = "main"
	sideEffectPhasePostPipeline = "post_pipeline"
	sideEffectPhaseOnCancel     = "on_cancel"
	sideEffectPhaseOutput       = "declared_output"

	sideEffectKindTmuxSend             = "tmux_send"
	sideEffectKindTemplateDispatch     = "template_dispatch"
	sideEffectKindShellCommand         = "shell_command"
	sideEffectKindAgentMailSend        = "agent_mail_send"
	sideEffectKindAgentMailReservation = "agent_mail_reservation"
	sideEffectKindAgentMailRelease     = "agent_mail_release"
	sideEffectKindAgentMailInboxCheck  = "agent_mail_inbox_check"
	sideEffectKindBeadQuery            = "bead_query"
	sideEffectKindFilesystemWrite      = "filesystem_write"
)

// SideEffectManifest is the dry-run contract for external effects a workflow
// can perform once executed. It is intentionally derived from the workflow
// definition rather than runtime state so robot callers can inspect it before
// dispatch begins.
type SideEffectManifest struct {
	Summary         SideEffectSummary `json:"summary"`
	Effects         []SideEffectEntry `json:"effects"`
	RollbackPreview []SideEffectEntry `json:"rollback_preview,omitempty"`
}

// SideEffectSummary gives robot callers a compact count of planned external
// effects without requiring them to scan every manifest entry.
type SideEffectSummary struct {
	Total           int            `json:"total"`
	ByKind          map[string]int `json:"by_kind"`
	CleanupActions  int            `json:"cleanup_actions,omitempty"`
	RollbackActions int            `json:"rollback_actions,omitempty"`
}

// SideEffectEntry describes one planned external effect or declared artifact.
type SideEffectEntry struct {
	StepID       string   `json:"step_id,omitempty"`
	ParentStepID string   `json:"parent_step_id,omitempty"`
	Phase        string   `json:"phase"`
	Kind         string   `json:"kind"`
	Description  string   `json:"description"`
	Target       string   `json:"target,omitempty"`
	Agent        string   `json:"agent,omitempty"`
	Pane         string   `json:"pane,omitempty"`
	Route        string   `json:"route,omitempty"`
	Command      string   `json:"command,omitempty"`
	Template     string   `json:"template,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Recipients   []string `json:"recipients,omitempty"`
	Subject      string   `json:"subject,omitempty"`
	ThreadID     string   `json:"thread_id,omitempty"`
	Cleanup      bool     `json:"cleanup,omitempty"`
	Rollback     bool     `json:"rollback,omitempty"`
}

type sideEffectContext struct {
	phase        string
	parentStepID string
	cleanup      bool
	rollback     bool
}

// BuildSideEffectManifest extracts a structured side-effect plan from a
// workflow. It does not validate or normalize; callers that load from disk
// should use LoadAndValidate first so alias fields and step IDs are canonical.
func BuildSideEffectManifest(workflow *Workflow) SideEffectManifest {
	manifest := SideEffectManifest{
		Effects: make([]SideEffectEntry, 0),
		Summary: SideEffectSummary{
			ByKind: make(map[string]int),
		},
	}
	if workflow == nil {
		return manifest
	}

	for i := range workflow.Steps {
		manifest.collectStep(workflow.Steps[i], sideEffectContext{phase: sideEffectPhaseMain})
	}
	for i, output := range workflow.Outputs {
		if strings.TrimSpace(output.Path) == "" {
			continue
		}
		name := output.Name
		if name == "" {
			name = fmt.Sprintf("output_%d", i+1)
		}
		manifest.add(SideEffectEntry{
			StepID:      name,
			Phase:       sideEffectPhaseOutput,
			Kind:        sideEffectKindFilesystemWrite,
			Description: firstNonEmpty(output.Description, "Declared workflow output path"),
			Target:      output.Path,
			Paths:       []string{output.Path},
		})
	}
	for i := range workflow.PostPipelineSteps {
		step := workflow.PostPipelineSteps[i]
		if step.ID == "" {
			step.ID = fmt.Sprintf("post_pipeline_%d", i+1)
		}
		manifest.collectStep(step, sideEffectContext{
			phase:   sideEffectPhasePostPipeline,
			cleanup: true,
		})
	}
	for i := range workflow.Settings.OnCancel {
		step := workflow.Settings.OnCancel[i]
		if step.ID == "" {
			step.ID = fmt.Sprintf("on_cancel_%d", i+1)
		}
		manifest.collectStep(step, sideEffectContext{
			phase:    sideEffectPhaseOnCancel,
			cleanup:  true,
			rollback: true,
		})
	}

	return manifest
}

func (m *SideEffectManifest) collectStep(step Step, ctx sideEffectContext) {
	switch {
	case step.Command != "":
		m.add(stepSideEffect(step, ctx, sideEffectKindShellCommand, "Run shell command", func(entry *SideEffectEntry) {
			entry.Command = step.Command
			entry.Target = "shell"
		}))
	case step.Template != "":
		m.add(stepSideEffect(step, ctx, sideEffectKindTemplateDispatch, "Dispatch rendered template to tmux pane", func(entry *SideEffectEntry) {
			entry.Template = step.Template
			populateStepTarget(entry, step)
		}))
	case step.Prompt != "" || step.PromptFile != "":
		m.add(stepSideEffect(step, ctx, sideEffectKindTmuxSend, "Send prompt to tmux pane", func(entry *SideEffectEntry) {
			populateStepTarget(entry, step)
			if step.PromptFile != "" {
				entry.Target = firstNonEmpty(entry.Target, "prompt_file:"+step.PromptFile)
			}
		}))
	case step.Branch != "":
		m.add(stepSideEffect(step, ctx, sideEffectKindShellCommand, "Evaluate branch predicate command", func(entry *SideEffectEntry) {
			entry.Command = step.Branch
			entry.Target = "branch predicate"
		}))
	case step.BeadQuery != nil:
		m.add(stepSideEffect(step, ctx, sideEffectKindBeadQuery, "Run structured br query", nil))
	case step.MailSend != nil:
		send := step.MailSend
		m.add(stepSideEffect(step, ctx, sideEffectKindAgentMailSend, "Send Agent Mail message", func(entry *SideEffectEntry) {
			entry.Target = send.ProjectKey
			entry.Agent = send.AgentName
			entry.Recipients = append([]string(nil), send.To...)
			entry.Subject = send.Subject
			entry.ThreadID = send.ThreadID
		}))
	case step.FileReservationPaths != nil:
		reserve := step.FileReservationPaths
		m.add(stepSideEffect(step, ctx, sideEffectKindAgentMailReservation, "Reserve files through Agent Mail", func(entry *SideEffectEntry) {
			entry.Target = reserve.ProjectKey
			entry.Agent = reserve.AgentName
			entry.Paths = append([]string(nil), reserve.Paths...)
		}))
	case step.FileReservationRelease != nil:
		release := step.FileReservationRelease
		m.add(stepSideEffect(step, ctx, sideEffectKindAgentMailRelease, "Release Agent Mail file reservations", func(entry *SideEffectEntry) {
			entry.Target = release.ProjectKey
			entry.Agent = release.AgentName
			entry.Paths = append([]string(nil), release.Paths...)
			entry.Cleanup = true
		}))
	case step.MailInboxCheck != nil:
		inbox := step.MailInboxCheck
		m.add(stepSideEffect(step, ctx, sideEffectKindAgentMailInboxCheck, "Check Agent Mail inbox", func(entry *SideEffectEntry) {
			entry.Target = inbox.ProjectKey
			entry.Agent = inbox.AgentName
		}))
	}

	childCtx := ctx
	if step.ID != "" {
		childCtx.parentStepID = step.ID
	}
	for i := range step.Parallel.Steps {
		m.collectStep(step.Parallel.Steps[i], childCtx)
	}
	if step.Loop != nil {
		for i := range step.Loop.Steps {
			m.collectStep(step.Loop.Steps[i], childCtx)
		}
	}
	for _, foreach := range []*ForeachConfig{step.Foreach, step.ForeachPane} {
		if foreach == nil {
			continue
		}
		if foreach.Template != "" {
			templateStep := Step{
				ID:       step.ID + "_foreach_template",
				Template: foreach.Template,
				Params:   foreach.Params,
			}
			m.collectStep(templateStep, childCtx)
		}
		for i := range foreach.Steps {
			m.collectStep(foreach.Steps[i], childCtx)
		}
	}
	for i := range step.OnSuccess {
		onSuccessCtx := childCtx
		onSuccessCtx.phase = ctx.phase
		m.collectStep(step.OnSuccess[i], onSuccessCtx)
	}
}

func (m *SideEffectManifest) add(entry SideEffectEntry) {
	entry.Description = firstNonEmpty(entry.Description, entry.Kind)
	m.Effects = append(m.Effects, entry)
	m.Summary.Total = len(m.Effects)
	if m.Summary.ByKind == nil {
		m.Summary.ByKind = make(map[string]int)
	}
	m.Summary.ByKind[entry.Kind]++
	if entry.Cleanup {
		m.Summary.CleanupActions++
	}
	if entry.Rollback {
		m.Summary.RollbackActions++
	}
	if entry.Cleanup || entry.Rollback {
		m.RollbackPreview = append(m.RollbackPreview, entry)
	}
}

func stepSideEffect(step Step, ctx sideEffectContext, kind, description string, fill func(*SideEffectEntry)) SideEffectEntry {
	entry := SideEffectEntry{
		StepID:       step.ID,
		ParentStepID: ctx.parentStepID,
		Phase:        ctx.phase,
		Kind:         kind,
		Description:  firstNonEmpty(step.Description, description),
		Cleanup:      ctx.cleanup,
		Rollback:     ctx.rollback,
	}
	if fill != nil {
		fill(&entry)
	}
	return entry
}

func populateStepTarget(entry *SideEffectEntry, step Step) {
	if step.Agent != "" {
		entry.Agent = NormalizeAgentType(step.Agent)
		entry.Target = "agent:" + entry.Agent
	}
	if !step.Pane.IsZero() {
		entry.Pane = paneSpecString(step.Pane)
		entry.Target = "pane:" + entry.Pane
	}
	if step.Route != "" {
		entry.Route = string(step.Route)
		entry.Target = "route:" + entry.Route
	}
}

func paneSpecString(p PaneSpec) string {
	if p.Expr != "" {
		return p.Expr
	}
	return fmt.Sprintf("%d", p.Index)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// RenderSideEffectManifestText returns a concise human preview for
// `ntm pipeline run --dry-run`.
func RenderSideEffectManifestText(manifest SideEffectManifest) string {
	var b strings.Builder
	if manifest.Summary.Total == 0 {
		b.WriteString("Side effects: none detected\n")
		return b.String()
	}

	fmt.Fprintf(&b, "Side effects: %d planned", manifest.Summary.Total)
	if kinds := formatSideEffectKinds(manifest.Summary.ByKind); kinds != "" {
		fmt.Fprintf(&b, " (%s)", kinds)
	}
	b.WriteString("\n")

	writeSideEffectLines(&b, manifest.Effects, 8)

	if len(manifest.RollbackPreview) > 0 {
		fmt.Fprintf(&b, "Rollback/cleanup: %d action(s)\n", len(manifest.RollbackPreview))
		writeSideEffectLines(&b, manifest.RollbackPreview, 5)
	}

	return b.String()
}

func writeSideEffectLines(b *strings.Builder, entries []SideEffectEntry, limit int) {
	for i, entry := range entries {
		if i >= limit {
			fmt.Fprintf(b, "  ... %d more\n", len(entries)-limit)
			return
		}
		fmt.Fprintf(b, "  - %s\n", formatSideEffectLine(entry))
	}
}

func formatSideEffectLine(entry SideEffectEntry) string {
	label := entry.StepID
	if label == "" {
		label = entry.Phase
	}

	detail := entry.Description
	switch entry.Kind {
	case sideEffectKindShellCommand:
		detail = firstNonEmpty(entry.Command, detail)
	case sideEffectKindFilesystemWrite:
		detail = firstNonEmpty(strings.Join(entry.Paths, ", "), entry.Target, detail)
	case sideEffectKindAgentMailSend:
		if len(entry.Recipients) > 0 {
			detail = "to " + strings.Join(entry.Recipients, ", ")
		}
	case sideEffectKindAgentMailReservation, sideEffectKindAgentMailRelease:
		if len(entry.Paths) > 0 {
			detail = strings.Join(entry.Paths, ", ")
		}
	case sideEffectKindTemplateDispatch:
		detail = firstNonEmpty(entry.Template, detail)
	}
	return fmt.Sprintf("[%s] %s: %s", sanitizeSideEffectText(label, 80), entry.Kind, sanitizeSideEffectText(detail, 120))
}

func formatSideEffectKinds(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%s=%d", kind, counts[kind]))
	}
	return strings.Join(parts, ", ")
}

func sanitizeSideEffectText(value string, limit int) string {
	value = SanitizeDescriptionForTerminal(strings.TrimSpace(value))
	if limit > 0 {
		value = truncatePrompt(value, limit)
	}
	return value
}
