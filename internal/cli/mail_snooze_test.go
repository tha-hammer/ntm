package cli

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// snoozes reads an agent's active and ended snoozes from the helpers'
// state.db, through the read the hook and the nudge use.
func (e *closureEnv) snoozes(t *testing.T, project, agent string) (active, ended []state.SnoozedMessage) {
	t.Helper()
	store, err := state.Open(e.stateDB())
	if err != nil {
		t.Fatalf("open state.db: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state.db: %v", err)
	}
	active, ended, err = store.SnoozedMessages(project, agent, time.Now())
	if err != nil {
		t.Fatalf("read snoozes: %v", err)
	}
	return active, ended
}

// snoozeAs runs ntm mail snooze in a helper process as agent, which is how an
// agent runs it from its own shell.
func (e *closureEnv) snoozeAs(agent string, args ...string) closureRun {
	e.set("AGENT_NAME", agent)
	return e.runHelper("execute", "", append([]string{"mail", "snooze"}, args...)...)
}

// Loop 6, Behavior 5: ntm mail snooze saves an agent's own snooze, or errors
// and changes nothing.
func TestClosure_MailSnoozeCommand(t *testing.T) {
	mail := newFakeAgentMail(t)
	mail.addMessage("BlueLake", agentmail.InboxMessage{ID: 81, From: "RedStone", Subject: "snooze me"})
	mail.addMessage("RedStone", agentmail.InboxMessage{ID: 82, From: "BlueLake", Subject: "not blue's"})
	env := newClosureEnv(t)
	env.useAgentMail(mail)
	env.clearIdentityEnv()
	project := env.workDir

	before := time.Now()
	run := env.snoozeAs("BlueLake", "81", "--until", "30m")
	if run.err != nil || !strings.Contains(run.stdout, "Snoozed message 81 until ") || !strings.Contains(run.stdout, "(in 30m)") {
		t.Fatalf("snooze: code %d, stdout %q, stderr %q", run.code, run.stdout, run.stderr)
	}
	active, ended := env.snoozes(t, project, "BlueLake")
	if len(active) != 1 || active[0].MessageID != 81 || len(ended) != 0 ||
		active[0].Until.Before(before.Add(29*time.Minute)) || active[0].Until.After(time.Now().Add(31*time.Minute)) {
		t.Fatalf("after snooze: active %+v, ended %+v", active, ended)
	}
	if active[0].From != "RedStone" || active[0].Subject != "snooze me" {
		t.Fatalf("the notice wasn't recorded from Agent Mail: %+v", active[0])
	}
	events := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient=BlueLake"))
	if len(events) != 1 || events[0].MessageID != 81 {
		t.Fatalf("events = %+v", events)
	}

	fetches := len(mail.callsFor("fetch_inbox"))
	if run := env.snoozeAs("BlueLake", "81", "--until", "2h"); run.err != nil {
		t.Fatalf("re-snooze: %v", run.err)
	}
	if again, _ := env.snoozes(t, project, "BlueLake"); len(again) != 1 || again[0].Until.Before(time.Now().Add(119*time.Minute)) {
		t.Fatalf("re-snooze didn't replace the time: %+v", again)
	}
	if n := len(mail.callsFor("fetch_inbox")); n != fetches {
		t.Fatalf("a message with a notice was checked with Agent Mail again: %d fetches, want %d", n, fetches)
	}

	run = env.snoozeAs("BlueLake", "81", "--until", "3h", "--json")
	var out struct {
		Success      bool      `json:"success"`
		MessageID    int64     `json:"message_id"`
		Agent        string    `json:"agent"`
		Project      string    `json:"project"`
		SnoozedUntil time.Time `json:"snoozed_until"`
	}
	if run.err != nil || json.Unmarshal([]byte(run.stdout), &out) != nil || !out.Success || out.MessageID != 81 ||
		out.Agent != "BlueLake" || out.Project != project || out.SnoozedUntil.Before(time.Now().Add(179*time.Minute)) {
		t.Fatalf("json: %+v from %q, %v", out, run.stdout, run.err)
	}
	standing, _ := env.snoozes(t, project, "BlueLake")

	for _, tc := range []struct {
		agent string
		args  []string
		want  string
	}{
		{"BlueLake", []string{"82", "--until", "30m"}, "message 82 isn't in BlueLake's Agent Mail inbox"},
		{"BlueLake", []string{"999", "--until", "30m"}, "message 999 isn't in BlueLake's Agent Mail inbox"},
		{"BlueLake", []string{"81", "--until", "2020-01-01T00:00:00Z"}, "snooze time must be in the future"},
		{"BlueLake", []string{"81", "--until", "soon"}, "--until must be an RFC3339 time"},
		{"BlueLake", []string{"0", "--until", "30m"}, "message id must be a positive number"},
		{"", []string{"81", "--until", "30m"}, "no Agent Mail name found"},
		{"SomeoneElse", []string{"81", "--until", "30m"}, "message 81 isn't in SomeoneElse's Agent Mail inbox"},
	} {
		run := env.snoozeAs(tc.agent, tc.args...)
		if run.err == nil || !strings.Contains(run.stdout+run.stderr, tc.want) {
			t.Errorf("%s %v: code %d, output %q; want an error with %q", tc.agent, tc.args, run.code, run.stdout+run.stderr, tc.want)
		}
	}
	if after, _ := env.snoozes(t, project, "BlueLake"); len(after) != 1 || !after[0].Until.Equal(standing[0].Until) {
		t.Fatalf("an error changed the snooze: %+v, was %+v", after, standing)
	}
	if other, _ := env.snoozes(t, project, "RedStone"); len(other) != 0 {
		t.Fatalf("RedStone has snoozes: %+v", other)
	}
}

// Loop 6, Behavior 1: --until takes an RFC3339 time or a duration from now.
func TestParseSnoozeUntil(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.FixedZone("EST", -5*3600))
	cases := []struct {
		raw  string
		want time.Time
	}{
		{"30m", now.Add(30 * time.Minute).UTC()},
		{" 2h ", now.Add(2 * time.Hour).UTC()},
		{"1d", now.Add(24 * time.Hour).UTC()},
		{"45s", now.Add(45 * time.Second).UTC()},
		{"2026-11-09T15:30:00-05:00", time.Date(2026, 11, 9, 20, 30, 0, 0, time.UTC)},
		{"-5m", now.Add(-5 * time.Minute).UTC()}, // parses; the future check rejects it
	}
	for _, tc := range cases {
		got, err := parseSnoozeUntil(tc.raw, now)
		if err != nil || !got.Equal(tc.want) || got.Location() != time.UTC {
			t.Errorf("parseSnoozeUntil(%q) = %v, %v; want %v in UTC", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{"soon", "", "30x", "tomorrow"} {
		_, err := parseSnoozeUntil(raw, now)
		if !errors.Is(err, errSnoozeUntilForm) {
			t.Errorf("parseSnoozeUntil(%q) error = %v, want errSnoozeUntilForm", raw, err)
		}
	}
	for _, want := range []string{"RFC3339", "2026-11-09T15:30:00Z", "30m", "2h", "1d"} {
		if !contains(errSnoozeUntilForm.Error(), want) {
			t.Errorf("the form error %q doesn't mention %q", errSnoozeUntilForm, want)
		}
	}
}

// Behavior 1 property: a duration, or an RFC3339 time, round-trips to the same
// instant.
func TestParseSnoozeUntil_RoundTripProperty(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	rapid.Check(t, func(rt *rapid.T) {
		d := time.Duration(rapid.Int64Range(1, 7*24*3600).Draw(rt, "seconds")) * time.Second
		if got, err := parseSnoozeUntil(d.String(), now); err != nil || !got.Equal(now.Add(d)) {
			rt.Fatalf("duration %s: got %v, %v", d, got, err)
		}
		at := now.Add(time.Duration(rapid.Int64Range(-1e6, 1e6).Draw(rt, "offset")) * time.Second)
		if got, err := parseSnoozeUntil(at.Format(time.RFC3339), now); err != nil || !got.Equal(at) {
			rt.Fatalf("time %s: got %v, %v", at, got, err)
		}
	})
}

// snoozeInSession runs ntm mail snooze as the session's agent, for the
// session's project, and fails the test if it fails.
func snoozeInSession(t *testing.T, env *closureEnv, session, agent string, id int, until string) {
	t.Helper()
	run := env.snoozeAs(agent, strconv.Itoa(id), "--until", until, "--project", env.projectDir(session))
	if run.err != nil {
		t.Fatalf("snooze %d: %v\nstdout: %s", id, run.err, run.stdout)
	}
}

// Loop 6, Behavior 6: the Stop hook leaves out an actively snoozed message,
// which was never told before it was snoozed, so only the snooze hides it.
func TestClosure_HookSkipsSnoozed(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "idle_nudge_enabled = false\n")
	_, agent := spawnNudgeAgent(t, env, agents, "hooksnooze")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 91, From: "RedStone", Subject: "snooze me", Importance: "normal"})
	mail.addMessage(agent, agentmail.InboxMessage{ID: 92, From: "RedStone", Subject: "second", Importance: "high"})
	snoozeInSession(t, env, "hooksnooze", agent, 91, "30m")

	decision := parseStopDecision(t, runStopHook(t, env, "hooksnooze", agent))
	if !strings.Contains(decision.Reason, "[#92] from RedStone: second") || strings.Contains(decision.Reason, "snooze me") {
		t.Fatalf("hook reason = %q; want only the second message", decision.Reason)
	}
	// 92 is told now, so only the snoozed message is left: the hook is silent.
	if out := runStopHook(t, env, "hooksnooze", agent); out != "" {
		t.Fatalf("hook with only snoozed mail printed %q", out)
	}
	if rec := env.told(t, env.projectDir("hooksnooze"), agent, 91); rec != nil {
		t.Fatalf("the snoozed message was claimed: %+v", rec)
	}
}

// Loop 6, Behavior 7: the nudge leaves out an actively snoozed message, and
// with only snoozed mail types nothing.
func TestClosure_NudgeSkipsSnoozed(t *testing.T) {
	t.Run("with other mail", func(t *testing.T) {
		mail := newFakeAgentMail(t)
		env := newClosureEnv(t)
		env.privateTmux(t)
		agents := env.screenAgentConfig(t, mail, "")
		pid, agent := spawnNudgeAgent(t, env, agents, "nudgesnooze")
		mail.addMessage(agent, agentmail.InboxMessage{ID: 93, From: "RedStone", Subject: "snooze me"})
		mail.addMessage(agent, agentmail.InboxMessage{ID: 94, From: "RedStone", Subject: "new one"})
		snoozeInSession(t, env, "nudgesnooze", agent, 93, "30m")
		env.startMonitor(t, "nudgesnooze")

		got := agents.waitTyped(t, pid, "new one", 15*time.Second)
		if !strings.Contains(got, "[#94]") || strings.Contains(got, "snooze me") || strings.Contains(got, "[#93]") {
			t.Fatalf("typed = %q; want only message 94", got)
		}
		if rec := env.told(t, env.projectDir("nudgesnooze"), agent, 93); rec != nil {
			t.Fatalf("the snoozed message was claimed: %+v", rec)
		}
	})
	t.Run("only snoozed mail", func(t *testing.T) {
		mail := newFakeAgentMail(t)
		env := newClosureEnv(t)
		env.privateTmux(t)
		agents := env.screenAgentConfig(t, mail, "")
		pid, agent := spawnNudgeAgent(t, env, agents, "onlysnooze")
		mail.addMessage(agent, agentmail.InboxMessage{ID: 95, From: "RedStone", Subject: "snooze me"})
		snoozeInSession(t, env, "onlysnooze", agent, 95, "30m")
		fetchesBefore := len(mail.callsFor("fetch_inbox"))
		env.startMonitor(t, "onlysnooze")
		time.Sleep(3 * time.Second)
		if typed := agents.typed(t, pid); strings.Contains(typed, "new message") {
			t.Fatalf("typed a reminder for snoozed mail: %q", typed)
		}
		if rec := env.told(t, env.projectDir("onlysnooze"), agent, 95); rec != nil {
			t.Fatalf("the snoozed message was claimed: %+v", rec)
		}
		if len(mail.callsFor("fetch_inbox")) == fetchesBefore {
			t.Fatal("the monitor never polled, so this test proves nothing")
		}
	})
}
