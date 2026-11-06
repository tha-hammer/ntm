# Loop 9: Release journey

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), section 6.

## Overview

Earlier loops prove each piece on its own. This loop proves the whole path works together, finishes the two end-to-end tests the scope requires, measures the scope's quality targets, and brings the contract document up to date.

Covers the end-to-end and doc parts of deliverable 18. The tests for each item were written in the sprint that built it. Needs all earlier loops.

---

## Sprint 1: The whole path, working

### Testable function

"Run the scope's Connected Release Journey by hand, and run the end-to-end tests for message to hook output and message to idle nudge, with `docs/attention-feed-contract.md` updated to match."

### Acceptance criteria

- [ ] The Connected Release Journey passes by hand on this VPS, with no dashboard or `ntm serve` running.
- [ ] Two end-to-end tests pass in `e2e/`: message to hook output, and message to idle nudge.
- [ ] The scope's Cross-Process Failure passes: with Agent Mail stopped mid-session, agents stop normally, nothing blocks, and reminders resume once it's back.
- [ ] `docs/attention-feed-contract.md` matches the code, including section 6.3 and the `retention_expires_at` field (see Requirements).
- [ ] The five quality targets in `SCOPE.md` section 6 are measured or checked, and results are recorded. Any miss is brought to you, not hidden.
- [ ] `go build ./cmd/ntm`, `go test -short ./...`, `golangci-lint run` and `gofmt -l .` all pass.

### Requirements

**Data:** None new.

**API:**
- New tests in `e2e/`, using the existing `//go:build e2e` tag and scenario harness (`e2e/scenario_harness_test.go`). They use a throwaway config folder, so the real `state.db` isn't touched.
- Any defect found here gets fixed in the code it belongs to, with a test, not worked around in the journey.

**Docs (`docs/attention-feed-contract.md`):**
- Section 6.3 says at least 1 hour; update it to 7 days.
- `cursor_retention_min_sec: 3600` at line 655 is an example of the old value; check whether it should change.
- The contract shows `retention_expires_at` in responses, but no robot code sets it, and `--robot-events` returns it as absent. **Assumption:** fix the doc to match the code, rather than adding the field. Bring it to you if you'd rather have the field.
- Add the recipient fields and `--events-recipient`, mail event uniqueness, durable-by-default behavior, and cross-process delivery.

**UI:** `--robot-capabilities` and `--robot-help` list the new flags. A short section in the main docs covers the hook, nudge, switches and snooze. **Assumption:** the TDD step picks the file.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Message to hook output (e2e) | Real or fake Agent Mail, deliver a message, run the hook command as the agent | Block JSON with sender and subject, then silence |
| Message to idle nudge (e2e) | Idle pane in a real tmux session running a stub agent, deliver a message, run the monitor | One reminder in the pane, then nothing |
| Restart mid-journey | Stop and restart the monitor and dashboard between messages | No events lost, and no reminders repeated |
| Agent Mail outage | Stop the server, end turns and poll, then restart it | Nothing blocks, and reminders resume |
| Hook speed | Run the command 20 times with no new mail | Under 1 second, and the actual number is recorded |
| Idle reminder delay | Time from message to reminder on an idle agent | About one poll, and the actual number is recorded |
| Event retention | Check an old event from Loop 1, Sprint 2 | Still readable, and expiry is 7 days out |
| Hook file safety | Spawn repeatedly in a folder with other entries | One entry, and others kept |
| Migration completeness | Search both machines for the old Stop script | None registered, once you've finished the local migration |

### Human test script

Do this in a new scratch git repo, with no dashboard or `ntm serve` running.

1. Build the binary: `go build -o /tmp/ntm-release ./cmd/ntm`.
2. Run `/tmp/ntm-release spawn release --cc=1`, and wait for the agent to be idle. Find its name with `/tmp/ntm-release --robot-status`.
3. Give it slow work: `/tmp/ntm-release send release --cc "Run sleep 60, then reply done."`.
4. While it works, send mail: `/tmp/ntm-release mail send release --to <agent> "mid turn"`.
5. Pass: when the turn ends, it's told about `mid turn`, with the sender and subject, and runs `fetch_inbox`.
6. In another process, run `/tmp/ntm-release --robot-events --events-recipient=<agent> | jq '.events[].summary'`.
7. Pass: the `mid turn` event is listed once.
8. Let the agent go idle, and send another message: `/tmp/ntm-release mail send release --to <agent> "while idle"`.
9. Pass: within about 30 seconds, one nudge names `while idle`.
10. Find the `internal-monitor` process for this session and kill it. Then kill and respawn the session in the same folder.
11. Pass: `/tmp/ntm-release --robot-events --events-recipient=<agent>` still lists both events, and neither message is announced again.
12. Run `go test -tags=e2e -v ./e2e/ -run <the two new test names>`.
13. Pass: both pass.
14. Read section 6.3 of `docs/attention-feed-contract.md`.
15. Pass: it says 7 days, and matches what you saw in step 6 of Loop 1, Sprint 2.
