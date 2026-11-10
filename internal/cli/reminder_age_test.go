package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// Loop 10, Behavior 1: a message is too old to announce only when it has a
// time and that time is more than ToldRecordRetention before now.
func TestTooOldToAnnounce(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name    string
		created time.Time
		want    bool
	}{
		{"8 days", now.Add(-8 * 24 * time.Hour), true},
		{"exactly 7 days", now.Add(-state.ToldRecordRetention), false},
		{"7 days and 1ns", now.Add(-state.ToldRecordRetention - 1), true},
		{"zero", time.Time{}, false},
		{"future", now.Add(time.Hour), false},
	} {
		if got := tooOldToAnnounce(c.created, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Behavior 1 property: for any age in [-30d, 30d], the message is too old
// exactly when its age is over ToldRecordRetention. A zero time never is.
func TestTooOldToAnnounce_Property(t *testing.T) {
	now := time.Now().UTC()
	rapid.Check(t, func(rt *rapid.T) {
		age := time.Duration(rapid.Int64Range(-30*24*int64(time.Hour), 30*24*int64(time.Hour)).Draw(rt, "age"))
		if got := tooOldToAnnounce(now.Add(-age), now); got != (age > state.ToldRecordRetention) {
			rt.Fatalf("age %s: got %v", age, got)
		}
		if tooOldToAnnounce(time.Time{}, now.Add(age)) {
			rt.Fatalf("a zero time was too old at now%+s", age)
		}
	})
}

// seedToldRecord writes a told record into the helpers' state.db through
// ClaimToldRecord, the call an earlier hook or nudge makes, and fails the test
// unless the claim wins.
func (e *closureEnv) seedToldRecord(t *testing.T, project, agent string, messageID int64, toldBy string, toldAt time.Time) {
	t.Helper()
	store, err := state.Open(e.stateDB())
	if err != nil {
		t.Fatalf("open state.db: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state.db: %v", err)
	}
	won, err := store.ClaimToldRecord(state.ToldRecord{ProjectKey: project, AgentName: agent, MessageID: messageID, ToldBy: toldBy, ToldAt: toldAt})
	if err != nil || !won {
		t.Fatalf("seed told record %d: won %v, err %v", messageID, won, err)
	}
}

// newHookClosure is a closure environment whose helpers reach a fake Agent
// Mail as agent, with a session registry holding the agent's token, so
// "ntm agent-hook stop --project env.workDir" fetches that agent's inbox.
func newHookClosure(t *testing.T, session, agent string) (*closureEnv, *fakeAgentMail) {
	t.Helper()
	mail := newFakeAgentMail(t)
	token := mail.issueToken(agent)
	env := newClosureEnv(t)
	env.useAgentMail(mail)
	env.seedRegistry(t, session, agent, token)
	env.set("AGENT_NAME", agent)
	return env, mail
}

// stopHook runs "ntm agent-hook stop" for the environment's work folder.
func (e *closureEnv) stopHook(t *testing.T) string {
	t.Helper()
	return e.runWithStdin(t, "{}", "execute", "agent-hook", "stop", "--project", e.workDir)
}

// Loop 10, Behavior 3: at turn end the hook says nothing about a message more
// than 7 days old whose told record has expired, rewrites no told record, and
// records no feed notice for it.
func TestClosure_AgeCutoffHookQuiet(t *testing.T) {
	env, mail := newHookClosure(t, "agecut", "BlueLake")
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	mail.addMessage("BlueLake", agentmail.InboxMessage{ID: 900, From: "RedStone", Subject: "old news", CreatedTS: agentmail.FlexTime{Time: old}})
	toldAt := time.Now().UTC().Add(-state.ToldRecordRetention - time.Minute)
	env.seedToldRecord(t, env.workDir, "BlueLake", 900, state.ToldByStopHook, toldAt)

	if out := env.stopHook(t); out != "" {
		t.Fatalf("hook printed %q, want nothing", out)
	}
	if rec := env.told(t, env.workDir, "BlueLake", 900); rec == nil || !rec.ToldAt.Equal(toldAt.Truncate(time.Second)) || rec.ToldBy != state.ToldByStopHook {
		t.Fatalf("told record = %+v, want unchanged (told_at %s)", rec, toldAt.Truncate(time.Second))
	}
	if ev := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient=BlueLake")); len(ev) != 0 {
		t.Fatalf("events = %+v, want none", ev)
	}
	if fetches := mail.callsFor("fetch_inbox"); len(fetches) != 1 {
		t.Fatalf("fetch_inbox calls = %+v, want 1: the hook must reach Agent Mail", fetches)
	}
}

// Loop 10, Behavior 4: the idle nudge types nothing about a message more than
// 7 days old whose told record has expired, over at least 5 polls, and leaves
// its told record alone. A fresh message sent afterwards is still typed, so
// the quiet window isn't a stalled monitor.
func TestClosure_AgeCutoffNudgeQuiet(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "agecutnudge")
	project := env.projectDir("agecutnudge")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 903, From: "RedStone", Subject: "old news",
		CreatedTS: agentmail.FlexTime{Time: time.Now().UTC().Add(-8 * 24 * time.Hour)}})
	toldAt := time.Now().UTC().Add(-state.ToldRecordRetention - time.Minute)
	env.seedToldRecord(t, project, agent, 903, state.ToldByIdleNudge, toldAt)

	env.startMonitor(t, "agecutnudge")
	time.Sleep(6 * time.Second)
	if typed := agents.typed(t, pid); strings.Contains(typed, "#903") || strings.Contains(typed, "new message") {
		t.Fatalf("nudge typed %q, want no reminder", typed)
	}
	if rec := env.told(t, project, agent, 903); rec == nil || !rec.ToldAt.Equal(toldAt.Truncate(time.Second)) || rec.ToldBy != state.ToldByIdleNudge {
		t.Fatalf("told record = %+v, want unchanged", rec)
	}

	mail.addMessage(agent, agentmail.InboxMessage{ID: 904, From: "RedStone", Subject: "fresh", CreatedTS: agentmail.FlexTime{Time: time.Now().UTC()}})
	typed := agents.waitTyped(t, pid, "[#904] from RedStone: fresh", 15*time.Second)
	if strings.Contains(typed, "#903") {
		t.Fatalf("nudge typed the old message with the fresh one: %q", typed)
	}
}

// Loop 10, Behavior 5: a message 6 days old with no told record is still told
// at turn end, once.
func TestClosure_AgeCutoffYoungMessageTold(t *testing.T) {
	env, mail := newHookClosure(t, "agecut", "BlueLake")
	mail.addMessage("BlueLake", agentmail.InboxMessage{ID: 901, From: "RedStone", Subject: "still fresh",
		CreatedTS: agentmail.FlexTime{Time: time.Now().UTC().Add(-6 * 24 * time.Hour)}})

	if reason := parseStopDecision(t, env.stopHook(t)).Reason; !strings.Contains(reason, "[#901] from RedStone: still fresh") {
		t.Fatalf("reason = %q, want #901", reason)
	}
	if rec := env.told(t, env.workDir, "BlueLake", 901); rec == nil || rec.ToldBy != state.ToldByStopHook {
		t.Fatalf("told record = %+v, want stop_hook", rec)
	}
	if out := env.stopHook(t); out != "" {
		t.Fatalf("second run printed %q, want nothing", out)
	}
}

// Loop 10, Behavior 6 (Sprint 1 failure check): a message dated 1 hour in the
// future is announced, with no warning in the hook log, and the told record
// and notice an earlier run left for another message don't change.
func TestClosure_AgeCutoffFutureTimeTold(t *testing.T) {
	env, mail := newHookClosure(t, "agecut", "BlueLake")
	now := time.Now().UTC()
	mail.addMessage("BlueLake", agentmail.InboxMessage{ID: 905, From: "RedStone", Subject: "already told",
		CreatedTS: agentmail.FlexTime{Time: now.Add(-2 * time.Hour)}})
	if reason := parseStopDecision(t, env.stopHook(t)).Reason; !strings.Contains(reason, "[#905]") {
		t.Fatalf("earlier run: reason = %q, want #905", reason)
	}
	otherTold := env.told(t, env.workDir, "BlueLake", 905)
	before := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient=BlueLake"))
	if otherTold == nil || len(before) != 1 || before[0].MessageID != 905 {
		t.Fatalf("earlier run left told %+v, events %+v", otherTold, before)
	}

	mail.addMessage("BlueLake", agentmail.InboxMessage{ID: 902, From: "RedStone", Subject: "from the future",
		CreatedTS: agentmail.FlexTime{Time: now.Add(time.Hour)}})
	reason := parseStopDecision(t, env.stopHook(t)).Reason
	if !strings.Contains(reason, "[#902] from RedStone: from the future") || strings.Contains(reason, "#905") {
		t.Fatalf("reason = %q, want only #902", reason)
	}
	if rec := env.told(t, env.workDir, "BlueLake", 902); rec == nil || rec.ToldBy != state.ToldByStopHook {
		t.Fatalf("told record for 902 = %+v, want stop_hook", rec)
	}
	if rec := env.told(t, env.workDir, "BlueLake", 905); rec == nil || *rec != *otherTold {
		t.Fatalf("told record for 905 = %+v, want unchanged %+v", rec, otherTold)
	}
	events := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient=BlueLake"))
	if len(events) != 2 || events[0] != before[0] || events[1].MessageID != 902 {
		t.Fatalf("events = %+v, want the earlier notice unchanged plus one for 902", events)
	}
	logData, err := os.ReadFile(filepath.Join(env.dataDir(), "ntm", "logs", agentHookLogName))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read hook log: %v", err)
	}
	if log := string(logData); strings.Contains(log, "level=WARN") || strings.Contains(log, "level=ERROR") {
		t.Fatalf("hook log has a warning: %s", log)
	}
}

// openStore opens and migrates the helpers' state.db, closed when the test
// ends.
func (e *closureEnv) openStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(e.stateDB())
	if err != nil {
		t.Fatalf("open state.db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state.db: %v", err)
	}
	return store
}

// Loop 10, Behavior 10: an ended snooze for a message that isn't in the fetch
// is told at turn end once, with the sender and subject its notice saved.
func TestClosure_EndedSnoozeOutsideFetchTold(t *testing.T) {
	env, mail := newHookClosure(t, "agecut", "BlueLake")
	seedEndedSnooze(t, env.openStore(t), env.workDir, "BlueLake", 911, "GreenCastle", "late", time.Now().UTC())

	if reason := parseStopDecision(t, env.stopHook(t)).Reason; strings.Count(reason, "[#911] from GreenCastle: late") != 1 {
		t.Fatalf("reason = %q, want #911 once", reason)
	}
	if out := env.stopHook(t); out != "" {
		t.Fatalf("second run printed %q, want nothing", out)
	}
	if fetches := mail.callsFor("fetch_inbox"); len(fetches) != 2 {
		t.Fatalf("fetch_inbox calls = %+v, want 2: the hook must reach Agent Mail", fetches)
	}
}

// seedNotice stores the mail notice an earlier hook left for agent, expiring
// at expires (notices last 7 days), and returns its cursor.
func (e *closureEnv) seedNotice(t *testing.T, project, agent string, id int64, from, subject string, expires time.Time) int64 {
	t.Helper()
	return seedMailNotice(t, e.openStore(t), project, agent, id, from, subject, expires.Add(-7*24*time.Hour), expires)
}

// Loop 10, Behavior 11 (Sprint 2 failure check): ntm mail snooze refuses a
// time less than SnoozeReturnMargin before the notice expires, and changes
// neither the snooze state nor the told record. The latest allowed time is
// accepted.
func TestClosure_SnoozeMarginRefused(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.useAgentMail(mail)
	env.clearIdentityEnv()
	project := env.workDir
	expires := time.Now().UTC().Add(2 * 24 * time.Hour).Truncate(time.Second)
	cursor := env.seedNotice(t, project, "BlueLake", 920, "RedStone", "snooze me", expires)
	toldAt := expires.Add(-7 * 24 * time.Hour)
	env.seedToldRecord(t, project, "BlueLake", 920, state.ToldByStopHook, toldAt)
	latest := expires.Add(-state.SnoozeReturnMargin)

	run := env.snoozeAs("BlueLake", "920", "--until", latest.Add(time.Minute).Format(time.RFC3339), "--project", project)
	want := "snooze would end after its notice expires: message 920's notice expires at " + expires.Format(time.RFC3339) +
		"; snooze until " + latest.Format(time.RFC3339) + " at the latest"
	if run.err == nil || !strings.Contains(run.stdout+run.stderr, want) {
		t.Fatalf("code %d, output %q; want an error with %q", run.code, run.stdout+run.stderr, want)
	}
	if item, err := env.openStore(t).GetAttentionItemStateByCursor(cursor); err != nil || item != nil {
		t.Fatalf("a refused snooze wrote state: %+v, %v", item, err)
	}
	if rec := env.told(t, project, "BlueLake", 920); rec == nil || !rec.ToldAt.Equal(toldAt) || rec.ToldBy != state.ToldByStopHook {
		t.Fatalf("told record = %+v, want unchanged", rec)
	}

	if run := env.snoozeAs("BlueLake", "920", "--until", latest.Format(time.RFC3339), "--project", project); run.err != nil {
		t.Fatalf("at the latest allowed time: code %d, output %q", run.code, run.stdout+run.stderr)
	}
	if active, _ := env.snoozes(t, project, "BlueLake"); len(active) != 1 || active[0].MessageID != 920 || !active[0].Until.Equal(latest) {
		t.Fatalf("active snoozes = %+v, want #920 until %s", active, latest)
	}
	if calls := mail.callsFor("fetch_inbox"); len(calls) != 0 {
		t.Fatalf("fetch_inbox calls = %+v; the notice was there, so Agent Mail shouldn't be asked", calls)
	}
}

// Loop 10, Behavior 12 (closure b): a message more than 7 days old, snoozed
// through ntm mail snooze until the latest time its notice allows, comes back
// in exactly one reminder from the idle nudge, and never again: not over 5
// more polls, and not at the next turn end.
func TestClosure_LateSnoozeReturnsOnce(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "latesnooze")
	project := env.projectDir("latesnooze")
	now := time.Now().UTC()
	mail.addMessage(agent, agentmail.InboxMessage{ID: 912, From: "RedStone", Subject: "late snooze",
		CreatedTS: agentmail.FlexTime{Time: now.Add(-state.ToldRecordRetention - 10*time.Second)}})
	// The notice the hook left about 7 days ago, a little after Agent Mail's created time.
	env.seedNotice(t, project, agent, 912, "RedStone", "late snooze", now.Add(state.SnoozeReturnMargin+6*time.Second))
	snoozeInSession(t, env, "latesnooze", agent, 912, now.Add(5*time.Second).Format(time.RFC3339))
	env.startMonitor(t, "latesnooze")

	const entry = "[#912] from RedStone: late snooze"
	waitTypedCount(t, agents, pid, entry, 1, 12*time.Second)
	agents.setScreen(t, pid, "cc_idle.txt")
	time.Sleep(6 * time.Second)
	if n := strings.Count(agents.typed(t, pid), entry); n != 1 {
		t.Fatalf("reminded %d times, want 1", n)
	}
	if out := runStopHook(t, env, "latesnooze", agent); out != "" {
		t.Fatalf("the hook repeated it: %q", out)
	}
	if rec := env.told(t, project, agent, 912); rec == nil || rec.ToldBy != state.ToldByIdleNudge {
		t.Fatalf("told record = %+v, want idle_nudge", rec)
	}
	if active, ended := env.snoozes(t, project, agent); len(active) != 0 || len(ended) != 0 {
		t.Fatalf("snoozes left: active %+v, ended %+v; want none", active, ended)
	}
}
