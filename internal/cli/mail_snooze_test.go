package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"pgregory.net/rapid"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// Loop 9, Behavior 10: README's "Mail reminders" section covers the turn-end
// hook, the idle nudge, the [agent_mail] switches and snoozing, and every key
// and command in it is real.
func TestREADMEMailReminders(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	_, section, ok := strings.Cut(readme, "\n#### Mail reminders\n")
	if !ok {
		t.Fatal("README has no Mail reminders section")
	}
	if coordination := strings.Index(readme, "### 4. Coordination, Reservations, and Human Oversight"); coordination < 0 || coordination > strings.Index(readme, "#### Mail reminders") {
		t.Fatal("the Mail reminders section isn't under Coordination, Reservations, and Human Oversight")
	}
	if end := strings.Index(section, "\n### "); end >= 0 {
		section = section[:end]
	}

	tags := map[string]bool{}
	agentMail := reflect.TypeOf(config.AgentMailConfig{})
	for i := 0; i < agentMail.NumField(); i++ {
		tag, _, _ := strings.Cut(agentMail.Field(i).Tag.Get("toml"), ",")
		tags[tag] = true
	}
	for _, key := range []string{"stop_hook_enabled", "idle_nudge_enabled", "idle_nudge_interval_seconds", "human_typing_quiet_seconds"} {
		if !strings.Contains(section, key) {
			t.Errorf("the section doesn't name the switch %s", key)
		}
	}
	for _, match := range regexp.MustCompile(`(?m)^([a-z_]+) = `).FindAllStringSubmatch(section, -1) {
		if !tags[match[1]] {
			t.Errorf("the section names %s, which isn't an [agent_mail] key", match[1])
		}
	}

	commands := 0
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		var cmd *cobra.Command
		var rest string
		switch {
		case strings.HasPrefix(line, "ntm mail snooze "):
			cmd, rest = newMailSnoozeCmd(), strings.TrimPrefix(line, "ntm mail snooze ")
		case strings.HasPrefix(line, "ntm agent-hook migrate"):
			cmd, rest = newAgentHookMigrateCmd(), strings.TrimPrefix(line, "ntm agent-hook migrate")
		default:
			continue
		}
		commands++
		if err := cmd.ParseFlags(strings.Fields(rest)); err != nil {
			t.Errorf("README command %q: %v", line, err)
		}
	}
	if commands < 2 {
		t.Errorf("the section shows %d snooze or migrate commands, want at least 2", commands)
	}
	if !strings.Contains(section, "(docs/agent-hook-migration.md)") {
		t.Error("the section doesn't link docs/agent-hook-migration.md")
	}
}

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
		{"BlueLake", []string{"81", "--until", "8d"}, "snooze time can be at most 7 days ahead"}, // Loop 6, Behavior 9
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

// seedMailNotice stores the mail notice for agent that the hook or the nudge
// records when it announces a message, at ts and expiring at expires, and
// returns its cursor.
func seedMailNotice(t *testing.T, store *state.Store, project, agent string, id int64, from, subject string, ts, expires time.Time) int64 {
	t.Helper()
	cursor, _, err := store.AppendAddressedAttentionEvent(&state.StoredAttentionEvent{
		Ts: ts, Category: "mail", EventType: "mail.received", Source: "agent_mail",
		Actionability: "interesting", Severity: "info", Summary: "mail for " + agent,
		Details:   fmt.Sprintf(`{"from":%q,"subject":%q,"message_id":%d}`, from, subject, id),
		ExpiresAt: &expires, RecipientProject: project, RecipientAgent: agent, MessageID: &id,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

// seedEndedSnooze stores a mail notice for agent whose snooze ended a minute
// before now, as a snooze that ran out leaves it.
func seedEndedSnooze(t *testing.T, store *state.Store, project, agent string, id int64, from, subject string, now time.Time) {
	t.Helper()
	ts := now.Add(-time.Hour)
	cursor := seedMailNotice(t, store, project, agent, id, from, subject, ts, ts.Add(7*24*time.Hour))
	until := now.Add(-time.Minute)
	if err := store.UpsertAttentionItemState(&state.AttentionItemState{ItemKey: fmt.Sprintf("cursor:%d", cursor), State: state.AttentionStateSnoozed, SnoozedUntil: &until}); err != nil {
		t.Fatal(err)
	}
}

// fakeSnoozes is a snoozeReader with fixed lists.
type fakeSnoozes struct {
	active, ended []state.SnoozedMessage
	err           error
}

func (f fakeSnoozes) SnoozedMessages(string, string, time.Time) ([]state.SnoozedMessage, []state.SnoozedMessage, error) {
	return f.active, f.ended, f.err
}

func announcedIDs(items []announceItem) (ids []int, ended []int) {
	for _, it := range items {
		ids = append(ids, it.msg.ID)
		if it.ended != nil {
			ended = append(ended, it.msg.ID)
		}
	}
	return ids, ended
}

// Loop 6, Behavior 11: the shared step drops active snoozes and adds ended
// ones from the store, with their sender and subject, once each.
func TestMailToAnnounce(t *testing.T) {
	now := time.Now().UTC()
	snoozes := fakeSnoozes{
		active: []state.SnoozedMessage{{MessageID: 52}},
		ended:  []state.SnoozedMessage{{MessageID: 53, From: "GreenCastle", Subject: "come back", ItemKey: "cursor:9"}},
	}
	msgs := []agentmail.InboxMessage{{ID: 51, From: "RedStone", Subject: "new"}, {ID: 52, Subject: "held"}}
	items := mailToAnnounce(snoozes, "/p", "BlueLake", msgs, now)
	ids, ended := announcedIDs(items)
	if fmt.Sprint(ids) != "[51 53]" || fmt.Sprint(ended) != "[53]" {
		t.Fatalf("ids %v, ended %v; want [51 53] and [53]", ids, ended)
	}
	if back := items[1]; back.msg.From != "GreenCastle" || back.msg.Subject != "come back" || back.ended.ItemKey != "cursor:9" {
		t.Fatalf("returned item = %+v", back)
	}

	// Fetched as well: listed once, as the ended snooze.
	items = mailToAnnounce(snoozes, "/p", "BlueLake", append(msgs, agentmail.InboxMessage{ID: 53, From: "GreenCastle", Subject: "come back"}), now)
	if ids, ended := announcedIDs(items); fmt.Sprint(ids) != "[51 53]" || fmt.Sprint(ended) != "[53]" {
		t.Fatalf("with 53 fetched: ids %v, ended %v", ids, ended)
	}

	// A store read error leaves the fetched mail as it is (Fail Open).
	items = mailToAnnounce(fakeSnoozes{err: errors.New("busy")}, "/p", "BlueLake", msgs, now)
	if ids, ended := announcedIDs(items); fmt.Sprint(ids) != "[51 52]" || len(ended) != 0 {
		t.Fatalf("on a read error: ids %v, ended %v", ids, ended)
	}
}

// Behavior 11 property: the result is the fetched mail minus active snoozes,
// plus ended snoozes, each id once. Loop 10, Behavior 2: with a random
// created time per message, plain mail too old to announce is left out too.
func TestMailToAnnounce_Property(t *testing.T) {
	now := time.Now().UTC()
	rapid.Check(t, func(rt *rapid.T) {
		ids := rapid.SliceOfNDistinct(rapid.IntRange(1, 20), 0, 20, rapid.ID).Draw(rt, "fetched")
		var msgs []agentmail.InboxMessage
		tooOld := map[int]bool{}
		for _, id := range ids {
			msg := agentmail.InboxMessage{ID: id}
			if rapid.Bool().Draw(rt, fmt.Sprintf("dated-%d", id)) {
				age := time.Duration(rapid.Int64Range(-30*24*int64(time.Hour), 30*24*int64(time.Hour)).Draw(rt, fmt.Sprintf("age-%d", id)))
				msg.CreatedTS = agentmail.FlexTime{Time: now.Add(-age)}
				tooOld[id] = age > state.ToldRecordRetention
			}
			msgs = append(msgs, msg)
		}
		var snoozes fakeSnoozes
		kind := map[int]string{}
		for id := 1; id <= 20; id++ {
			switch rapid.IntRange(0, 2).Draw(rt, fmt.Sprintf("snooze-%d", id)) {
			case 1:
				snoozes.active = append(snoozes.active, state.SnoozedMessage{MessageID: int64(id)})
				kind[id] = "active"
			case 2:
				snoozes.ended = append(snoozes.ended, state.SnoozedMessage{MessageID: int64(id)})
				kind[id] = "ended"
			}
		}
		want := map[int]bool{}
		for _, id := range ids {
			if kind[id] == "" && !tooOld[id] {
				want[id] = true
			}
		}
		for id, k := range kind {
			if k == "ended" {
				want[id] = true
			}
		}
		items := mailToAnnounce(snoozes, "/p", "BlueLake", msgs, now)
		for _, it := range items {
			if it.ended == nil && tooOldToAnnounce(it.msg.CreatedTS.Time, now) {
				rt.Fatalf("plain item #%d is too old to announce (created %s)", it.msg.ID, it.msg.CreatedTS.Time)
			}
		}
		got, _ := announcedIDs(items)
		seen := map[int]bool{}
		for _, id := range got {
			if seen[id] || !want[id] {
				rt.Fatalf("got %v, want the set %v", got, want)
			}
			seen[id] = true
		}
		if len(seen) != len(want) {
			rt.Fatalf("got %v, want the set %v", got, want)
		}
	})
}

// Behavior 11: the hook claims an ended snooze through the store and names it
// in the reminder, even with no new mail.
func TestClaimUnread_EndedSnooze(t *testing.T) {
	mail := newFakeAgentMail(t)
	withHookTestEnv(t, mail, "BlueLake")
	store, err := openProcessStateStore()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seedEndedSnooze(t, store, "/proj", "BlueLake", 53, "GreenCastle", "come back", now)
	mail.addMessage("BlueLake", agentmail.InboxMessage{ID: 51, From: "RedStone", Subject: "new"})

	var out bytes.Buffer
	runAgentHookStop(strings.NewReader("{}"), &out, "/proj")
	reason := parseStopDecision(t, out.String()).Reason
	if !strings.Contains(reason, "[#51] from RedStone: new") || !strings.Contains(reason, "[#53] from GreenCastle: come back") {
		t.Fatalf("reason = %q; want both", reason)
	}
	if rec, _ := store.GetToldRecord("/proj", "BlueLake", 53); rec == nil || rec.ToldBy != state.ToldByStopHook {
		t.Fatalf("told record for 53 = %+v", rec)
	}
	if _, ended, _ := store.SnoozedMessages("/proj", "BlueLake", time.Now()); len(ended) != 0 {
		t.Fatalf("the snooze wasn't cleared: %+v", ended)
	}
	out.Reset()
	runAgentHookStop(strings.NewReader("{}"), &out, "/proj")
	if out.Len() != 0 {
		t.Fatalf("second run printed %q", out.String())
	}
}

// Loop 10, Behavior 2: the shared step leaves out plain mail more than
// ToldRecordRetention old, keeps mail with no time or a future time, in order,
// and does so even when the snoozes can't be read.
func TestMailToAnnounce_AgeCutoff(t *testing.T) {
	now := time.Now().UTC()
	at := func(d time.Duration) agentmail.FlexTime { return agentmail.FlexTime{Time: now.Add(d)} }
	msgs := []agentmail.InboxMessage{
		{ID: 61, CreatedTS: at(-8 * 24 * time.Hour)},
		{ID: 62, CreatedTS: at(-6 * 24 * time.Hour)},
		{ID: 63},
		{ID: 64, CreatedTS: at(time.Hour)},
	}
	if ids, _ := announcedIDs(mailToAnnounce(fakeSnoozes{}, "/p", "BlueLake", msgs, now)); fmt.Sprint(ids) != "[62 63 64]" {
		t.Fatalf("ids = %v, want [62 63 64]", ids)
	}
	if ids, _ := announcedIDs(mailToAnnounce(fakeSnoozes{err: errors.New("busy")}, "/p", "BlueLake", msgs, now)); fmt.Sprint(ids) != "[62 63 64]" {
		t.Fatalf("on a read error: ids = %v, want [62 63 64]", ids)
	}
}

// Loop 10, Behavior 9: a message back from a snooze is kept whatever its age,
// once, marked ended, as the fetched message in its fetched place.
func TestMailToAnnounce_EndedSnoozeIgnoresAge(t *testing.T) {
	now := time.Now().UTC()
	created := agentmail.FlexTime{Time: now.Add(-8 * 24 * time.Hour)}
	snoozes := fakeSnoozes{ended: []state.SnoozedMessage{{MessageID: 910, From: "GreenCastle", Subject: "late"}}}
	msgs := []agentmail.InboxMessage{
		{ID: 910, From: "GreenCastle", Subject: "late", Importance: "high", CreatedTS: created},
		{ID: 911, From: "RedStone", Subject: "fresh", CreatedTS: agentmail.FlexTime{Time: now}},
	}
	items := mailToAnnounce(snoozes, "/p", "BlueLake", msgs, now)
	ids, ended := announcedIDs(items)
	if fmt.Sprint(ids) != "[910 911]" || fmt.Sprint(ended) != "[910]" {
		t.Fatalf("ids %v ended %v; want [910 911] and [910]", ids, ended)
	}
	if got := items[0].msg; got.Importance != "high" || !got.CreatedTS.Time.Equal(created.Time) {
		t.Fatalf("returned item = %+v; want the fetched message", got)
	}
}

// Behavior 9 property: every ended snooze is returned exactly once, marked
// ended, whatever the age of its fetched message, and fetched or not.
func TestMailToAnnounce_EndedSnoozeAgeProperty(t *testing.T) {
	now := time.Now().UTC()
	rapid.Check(t, func(rt *rapid.T) {
		ids := rapid.SliceOfNDistinct(rapid.IntRange(1, 20), 1, 20, rapid.ID).Draw(rt, "ended")
		var snoozes fakeSnoozes
		var msgs []agentmail.InboxMessage
		for _, id := range ids {
			snoozes.ended = append(snoozes.ended, state.SnoozedMessage{MessageID: int64(id)})
			if rapid.Bool().Draw(rt, fmt.Sprintf("fetched-%d", id)) {
				age := time.Duration(rapid.Int64Range(-30*24*int64(time.Hour), 30*24*int64(time.Hour)).Draw(rt, fmt.Sprintf("age-%d", id)))
				msgs = append(msgs, agentmail.InboxMessage{ID: id, CreatedTS: agentmail.FlexTime{Time: now.Add(-age)}})
			}
		}
		count := map[int]int{}
		for _, it := range mailToAnnounce(snoozes, "/p", "BlueLake", msgs, now) {
			if it.ended == nil {
				rt.Fatalf("#%d returned unmarked", it.msg.ID)
			}
			count[it.msg.ID]++
		}
		for _, id := range ids {
			if count[id] != 1 {
				rt.Fatalf("#%d returned %d times, want 1 (counts %v)", id, count[id], count)
			}
		}
	})
}

// waitTypedCount waits until want appears n times in the stand-in's typed
// file, and returns the file.
func waitTypedCount(t *testing.T, agents *screenAgents, pid, want string, n int, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := agents.typed(t, pid)
		if strings.Count(got, want) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("stand-in %s: %q typed %d times within %s, want %d (typed %q)", pid, want, strings.Count(got, want), within, n, got)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Loop 6, Behavior 12: a snooze that ends while the agent is idle returns in
// one reminder, and nothing more follows, from the nudge or the hook.
func TestClosure_SnoozeReturnsOnce(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "returnonce")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 95, From: "RedStone", Subject: "come back"})
	env.startMonitor(t, "returnonce")
	const entry = "[#95] from RedStone: come back"
	waitTypedCount(t, agents, pid, entry, 1, 15*time.Second)
	agents.setScreen(t, pid, "cc_idle.txt")
	first := env.told(t, env.projectDir("returnonce"), agent, 95)

	snoozeInSession(t, env, "returnonce", agent, 95, "4s")
	time.Sleep(2 * time.Second)
	if n := strings.Count(agents.typed(t, pid), entry); n != 1 {
		t.Fatalf("reminded %d times while the snooze was active, want 1", n)
	}
	waitTypedCount(t, agents, pid, entry, 2, 12*time.Second)
	agents.setScreen(t, pid, "cc_idle.txt")
	if rec := env.told(t, env.projectDir("returnonce"), agent, 95); rec == nil || rec.ToldBy != state.ToldByIdleNudge || !rec.ToldAt.After(first.ToldAt) {
		t.Fatalf("told record after the return = %+v, first %+v", rec, first)
	}
	time.Sleep(3 * time.Second)
	if n := strings.Count(agents.typed(t, pid), entry); n != 2 {
		t.Fatalf("reminded %d times after the return, want 2", n)
	}
	if out := runStopHook(t, env, "returnonce", agent); out != "" {
		t.Fatalf("the hook repeated the returned message: %q", out)
	}
}

// Loop 6, Behavior 13: a snooze that ends during a turn is told at the turn
// end, and the nudge doesn't repeat it once the agent is idle.
func TestClosure_SnoozeEndsMidTurn(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "midturn")
	agents.setScreen(t, pid, "cc_working.txt")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 96, From: "RedStone", Subject: "after your turn"})
	if d := parseStopDecision(t, runStopHook(t, env, "midturn", agent)); !strings.Contains(d.Reason, "[#96]") {
		t.Fatalf("first hook reason = %q", d.Reason)
	}
	snoozeInSession(t, env, "midturn", agent, 96, "2s")
	env.startMonitor(t, "midturn")

	time.Sleep(4 * time.Second)
	if typed := agents.typed(t, pid); strings.Contains(typed, "[#96]") {
		t.Fatalf("typed into a working agent: %q", typed)
	}
	if d := parseStopDecision(t, runStopHook(t, env, "midturn", agent)); !strings.Contains(d.Reason, "[#96] from RedStone: after your turn") {
		t.Fatalf("turn-end reason = %q; want the returned message", d.Reason)
	}
	agents.setScreen(t, pid, "cc_idle.txt")
	time.Sleep(3 * time.Second)
	if typed := agents.typed(t, pid); strings.Contains(typed, "[#96]") {
		t.Fatalf("the nudge repeated what the hook told: %q", typed)
	}
}

// Loop 6, Behavior 14: a monitor restart keeps the snooze, which lives in
// state.db, and the message returns on time.
func TestClosure_SnoozeSurvivesMonitorRestart(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "restartsnooze")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 97, From: "RedStone", Subject: "still here"})
	if d := parseStopDecision(t, runStopHook(t, env, "restartsnooze", agent)); !strings.Contains(d.Reason, "[#97]") {
		t.Fatalf("first hook reason = %q", d.Reason)
	}
	snoozeInSession(t, env, "restartsnooze", agent, 97, "6s")
	until := time.Now().Add(6 * time.Second)

	stop := env.startMonitor(t, "restartsnooze")
	time.Sleep(time.Second)
	stop()
	env.startMonitor(t, "restartsnooze")
	time.Sleep(1500 * time.Millisecond)
	if time.Now().Before(until) && strings.Contains(agents.typed(t, pid), "[#97]") {
		t.Fatalf("the new monitor typed the message before its snooze ended: %q", agents.typed(t, pid))
	}
	agents.waitTyped(t, pid, "[#97] from RedStone: still here", 15*time.Second)
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
