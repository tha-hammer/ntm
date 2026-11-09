package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// The idle nudge: a session's monitor types one reminder into an idle agent's
// pane when the agent has mail it hasn't been told about. It shares the told
// record with the Stop hook, so each message is told once, by whichever comes
// first.

// idleNudgeFetchTimeout bounds one Agent Mail fetch, so a slow server can't
// hold up the other agents in the poll.
const idleNudgeFetchTimeout = 10 * time.Second

// nudgeTarget is one agent pane the poll looks at.
type nudgeTarget struct {
	PaneID    string
	AgentType tmux.AgentType
	Agent     string
	Pane      tmux.Pane // as listed in this poll, for the live-agent check
}

// paneNudgeState is the PaneNudgeState contract: what the poll knows about a
// pane just before typing, plus whether an agent is running there at all.
type paneNudgeState struct {
	Live bool // an agent process, not a bare shell, is in the pane
	Idle bool
}

// idleNudgeStore is the part of the state store the nudge uses.
type idleNudgeStore interface {
	ClaimToldRecord(state.ToldRecord) (bool, error)
	ReleaseToldRecord(state.ToldRecord) error
}

// idleNudgeDeps are the nudge's ways of reaching the outside world.
type idleNudgeDeps struct {
	ProjectKey string
	Targets    func(ctx context.Context) ([]nudgeTarget, error)
	Fetch      func(ctx context.Context, agent string) ([]agentmail.InboxMessage, error)
	PaneState  func(ctx context.Context, target nudgeTarget) (paneNudgeState, error)
	Store      func() (idleNudgeStore, error)
	Record     func(agent string, msg agentmail.InboxMessage)
	Send       func(target nudgeTarget, text string) error
	Now        func() time.Time
}

// idleNudger runs the poll and remembers what it has already logged between
// polls, so a state that lasts, such as a pane with no agent or a session with
// no registry yet, is logged once rather than every poll.
type idleNudger struct {
	deps           idleNudgeDeps
	notLive        map[string]bool
	lastTargetsErr string
}

func newIdleNudger(deps idleNudgeDeps) *idleNudger {
	return &idleNudger{deps: deps, notLive: map[string]bool{}}
}

// idleNudgeTicker makes the loop's ticker: its channel and its stop function.
type idleNudgeTicker func(interval time.Duration) (<-chan time.Time, func())

func timeIdleNudgeTicker(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// runIdleNudgeLoop polls on every tick until ctx ends.
func runIdleNudgeLoop(ctx context.Context, interval time.Duration, newTicker idleNudgeTicker, nudger *idleNudger) {
	ticks, stop := newTicker(interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			nudger.poll(ctx)
		}
	}
}

// poll looks at every agent pane once. A failure for one agent is logged and
// leaves the others to run.
func (n *idleNudger) poll(ctx context.Context) {
	targets, err := n.deps.Targets(ctx)
	if err != nil {
		if err.Error() != n.lastTargetsErr {
			n.lastTargetsErr = err.Error()
			slog.Warn("idle nudge: list agents failed", "error", err)
		}
		return
	}
	n.lastTargetsErr = ""
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		n.nudge(ctx, target)
	}
}

// nudge types one reminder for target's untold mail. The order is the scope's:
// find messages, check the pane, claim, then send, so the busy and live checks
// are as fresh as possible and a lost claim types nothing.
func (n *idleNudger) nudge(ctx context.Context, target nudgeTarget) {
	msgs, err := n.deps.Fetch(ctx, target.Agent)
	if err != nil {
		n.warn("fetch inbox", target, err)
		return
	}
	msgs = unreadMessages(msgs)
	if len(msgs) == 0 {
		return
	}

	pane, err := n.deps.PaneState(ctx, target)
	if err != nil {
		n.warn("read pane", target, err)
		return
	}
	if !pane.Live {
		// Reminder text holds other agents' subjects, so it must never reach a
		// shell prompt.
		if !n.notLive[target.PaneID] {
			n.notLive[target.PaneID] = true
			slog.Info("idle nudge: no agent running in pane, not typing", "pane", target.PaneID, "agent", target.Agent)
		}
		return
	}
	delete(n.notLive, target.PaneID)
	if !pane.Idle {
		return // the Stop hook tells a busy agent when its turn ends
	}

	store, err := n.deps.Store()
	if err != nil {
		// Without a claim, every poll would type the same reminder again. The
		// Stop hook still tells the agent at its next turn end.
		n.warn("open state store", target, err)
		return
	}
	claims, err := n.claim(store, target, msgs)
	if err != nil {
		n.warn("claim", target, err)
		n.release(store, target, claims)
		return
	}
	if len(claims) == 0 {
		return
	}

	told := make([]agentmail.InboxMessage, 0, len(claims))
	ids := make([]int, 0, len(claims))
	for _, c := range claims {
		n.deps.Record(target.Agent, c.msg)
		told = append(told, c.msg)
		ids = append(ids, c.msg.ID)
	}
	if err := n.deps.Send(target, buildReminderText(told, target.Agent, n.deps.ProjectKey)); err != nil {
		n.warn("send reminder", target, err)
		n.release(store, target, claims) // the next poll tries again
		return
	}
	slog.Info("idle nudge: reminder sent", "pane", target.PaneID, "agent", target.Agent, "messages", fmt.Sprint(ids))
}

// nudgeClaim is one message this poll won the right to tell.
type nudgeClaim struct {
	msg agentmail.InboxMessage
	rec state.ToldRecord
}

// claim claims each message for the nudge and returns the ones it won. On an
// error it returns the claims made so far, for release.
func (n *idleNudger) claim(store idleNudgeStore, target nudgeTarget, msgs []agentmail.InboxMessage) ([]nudgeClaim, error) {
	now := n.deps.Now().UTC()
	var won []nudgeClaim
	for _, msg := range msgs {
		rec := state.ToldRecord{
			ProjectKey: n.deps.ProjectKey,
			AgentName:  target.Agent,
			MessageID:  int64(msg.ID),
			ToldBy:     state.ToldByIdleNudge,
			ToldAt:     now,
		}
		ok, err := store.ClaimToldRecord(rec)
		if err != nil {
			return won, fmt.Errorf("message %d: %w", msg.ID, err)
		}
		if ok {
			won = append(won, nudgeClaim{msg: msg, rec: rec})
		}
	}
	return won, nil
}

func (n *idleNudger) release(store idleNudgeStore, target nudgeTarget, claims []nudgeClaim) {
	for _, c := range claims {
		if err := store.ReleaseToldRecord(c.rec); err != nil {
			n.warn("release claim", target, err)
		}
	}
}

func (n *idleNudger) warn(step string, target nudgeTarget, err error) {
	slog.Warn("idle nudge: "+step+" failed", "pane", target.PaneID, "agent", target.Agent, "error", err)
}

func unreadMessages(msgs []agentmail.InboxMessage) []agentmail.InboxMessage {
	unread := make([]agentmail.InboxMessage, 0, len(msgs))
	for _, msg := range msgs {
		if msg.ReadAt == nil {
			unread = append(unread, msg)
		}
	}
	return unread
}

// productionIdleNudgeDeps wires the nudge to the session's panes and agent
// registry, Agent Mail, tmux and the state store.
func productionIdleNudgeDeps(session string, manifest *resilience.SpawnManifest) idleNudgeDeps {
	projectKey := manifest.ProjectDir
	return idleNudgeDeps{
		ProjectKey: projectKey,
		Targets: func(ctx context.Context) ([]nudgeTarget, error) {
			panes, err := tmux.GetPanesContext(ctx, session)
			if err != nil {
				return nil, fmt.Errorf("list panes: %w", err)
			}
			// Read on every poll, so agents added while the monitor runs are found.
			registry, err := agentmail.LoadBestSessionAgentRegistry(session, projectKey)
			if err != nil {
				return nil, fmt.Errorf("load agent registry: %w", err)
			}
			if registry == nil {
				return nil, errNoAgentRegistry
			}
			return nudgeTargets(registry, panes), nil
		},
		Fetch: func(ctx context.Context, agent string) ([]agentmail.InboxMessage, error) {
			ctx, cancel := context.WithTimeout(ctx, idleNudgeFetchTimeout)
			defer cancel()
			return newAgentMailClient(projectKey).FetchInbox(ctx, agentmail.FetchInboxOptions{
				ProjectKey: projectKey,
				AgentName:  agent,
				Limit:      stopHookFetchLimit,
			})
		},
		PaneState: func(ctx context.Context, target nudgeTarget) (paneNudgeState, error) {
			screen, err := tmux.CaptureForStatusDetectionContext(ctx, target.PaneID)
			if err != nil {
				return paneNudgeState{}, err
			}
			return paneNudgeState{
				Live: tmux.PaneAgentProcess(target.Pane).Canonical() == target.AgentType.Canonical(),
				Idle: determineAgentState(screen, string(target.AgentType)) == "idle",
			}, nil
		},
		Store: func() (idleNudgeStore, error) {
			return openProcessStateStore()
		},
		Record: func(agent string, msg agentmail.InboxMessage) {
			robot.GetAttentionFeed().RecordMailNotice(projectKey, agent, msg)
		},
		Send: func(target nudgeTarget, text string) error {
			return tmux.SendKeysForAgentDoubleEnter(target.PaneID, text, target.AgentType)
		},
		Now: time.Now,
	}
}

// errNoAgentRegistry means the session has no agent registry yet, so no agent
// has a name to fetch mail for.
var errNoAgentRegistry = errors.New("session has no agent registry yet")

// nudgeableAgentTypes are the agents whose input line takes a pasted prompt and
// whose idle screen ntm recognizes.
var nudgeableAgentTypes = map[tmux.AgentType]bool{tmux.AgentClaude: true, tmux.AgentCodex: true}

// nudgeTargets joins the registry's pane IDs with the session's live panes.
// The registry, not the spawn manifest, is the source, because ntm add records
// added agents there and not in the manifest. Matching by pane ID means a
// reminder reaches exactly the agent it names. A pane's type is the one ntm set
// on it (@ntm_type, or its title).
func nudgeTargets(registry *agentmail.SessionAgentRegistry, panes []tmux.Pane) []nudgeTarget {
	var targets []nudgeTarget
	for _, pane := range panes {
		name := registry.PaneIDMap[pane.ID]
		if name == "" || !nudgeableAgentTypes[pane.Type.Canonical()] {
			continue
		}
		targets = append(targets, nudgeTarget{PaneID: pane.ID, AgentType: pane.Type, Agent: name, Pane: pane})
	}
	return targets
}
