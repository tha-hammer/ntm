package agentmail

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

const (
	liveInboxFixture  = "testdata/fetch_inbox_live_2026-11-10.json"
	liveInboxExpected = "testdata/fetch_inbox_live_2026-11-10.expected.txt"
)

// liveCreatedTSLayout is the created_ts text the live Agent Mail
// (mcp-agent-mail-rust) sent on 2026-11-10: microseconds and a +00:00 zone.
var liveCreatedTSLayout = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}\+00:00$`)

type liveExpected struct {
	raw     string
	instant time.Time
}

// readLiveExpected reads "id<TAB>raw created_ts<TAB>UTC instant" lines.
func readLiveExpected(t *testing.T) map[int]liveExpected {
	t.Helper()
	f, err := os.Open(liveInboxExpected)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want := map[int]liveExpected{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("bad expected line %q", line)
		}
		id, err := strconv.Atoi(parts[0])
		if err != nil {
			t.Fatalf("bad id in %q: %v", line, err)
		}
		instant, err := time.Parse(time.RFC3339Nano, parts[2])
		if err != nil {
			t.Fatalf("bad instant in %q: %v", line, err)
		}
		want[id] = liveExpected{raw: parts[1], instant: instant}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return want
}

// Loop 7, Behavior 7: a fetch_inbox response captured from the live Agent
// Mail, served unchanged to the real FetchInbox, decodes each created_ts to
// the UTC instant recorded next to the fixture.
func TestFlexTime_LiveFetchInboxCreatedTS(t *testing.T) {
	body, err := os.ReadFile(liveInboxFixture)
	if err != nil {
		t.Fatal(err)
	}
	want := readLiveExpected(t)
	if len(want) != 2 {
		t.Fatalf("expected file lists %d messages, want 2", len(want))
	}

	// The raw text must still be the captured layout, so the test pins it.
	var raw struct {
		Result struct {
			StructuredContent struct {
				Result []struct {
					ID        int    `json:"id"`
					CreatedTS string `json:"created_ts"`
				} `json:"result"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, m := range raw.Result.StructuredContent.Result {
		if !liveCreatedTSLayout.MatchString(m.CreatedTS) || m.CreatedTS != want[m.ID].raw {
			t.Fatalf("message %d: raw created_ts %q, want %q in the captured layout", m.ID, m.CreatedTS, want[m.ID].raw)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	t.Setenv("AGENT_MAIL_TOKEN", "")
	client := NewClient(WithBaseURL(srv.URL + "/mcp/"))
	msgs, err := client.FetchInbox(context.Background(), FetchInboxOptions{ProjectKey: "/p", AgentName: "RoseMeadow", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("decoded %d messages, want 2", len(msgs))
	}
	for _, m := range msgs {
		w, ok := want[m.ID]
		if !ok || !m.CreatedTS.Time.Equal(w.instant) || m.CreatedTS.Time.UTC() != w.instant.UTC() {
			t.Fatalf("message %d: CreatedTS %s, want %s", m.ID, m.CreatedTS.Time, w.instant)
		}
	}
}

// Behavior 7 property: FlexTime round-trips any time to the microsecond, and
// a zone-less string decodes as UTC.
func TestFlexTime_RoundTripProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		sec := rapid.Int64Range(0, 4102444800).Draw(rt, "sec") // 1970 to 2100
		usec := rapid.Int64Range(0, 999999).Draw(rt, "usec")
		// RFC3339 writes whole-minute zone offsets, as real zones have.
		offsetMin := rapid.IntRange(-14*60, 14*60).Draw(rt, "offset_minutes")
		at := time.Unix(sec, usec*1000).In(time.FixedZone("z", offsetMin*60))

		data, err := json.Marshal(FlexTime{Time: at})
		if err != nil {
			rt.Fatal(err)
		}
		var back FlexTime
		if err := json.Unmarshal(data, &back); err != nil {
			rt.Fatalf("unmarshal %s: %v", data, err)
		}
		if !back.Time.Equal(at) {
			rt.Fatalf("round trip %s -> %s -> %s", at, data, back.Time)
		}

		bare, _ := json.Marshal(at.UTC().Format("2006-01-02T15:04:05.999999"))
		var fromBare FlexTime
		if err := json.Unmarshal(bare, &fromBare); err != nil {
			rt.Fatalf("unmarshal %s: %v", bare, err)
		}
		if fromBare.Time != at.UTC() {
			rt.Fatalf("zone-less %s decoded to %s, want %s", bare, fromBare.Time, at.UTC())
		}
	})
}
