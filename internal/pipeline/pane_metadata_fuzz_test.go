package pipeline

// Fuzz suite for the pane metadata roster parsers — extends the
// pipeline_fuzz_test.go coverage that owns bd-7ramj.17 to a previously
// unfuzzed attack surface: parsePaneRosterYAML (the YAML structured
// roster decoder) and extractRosterYAMLBlock (the markdown "## Roster"
// section extractor used by RESUME.md and phase0_scope_decision.md).
//
// Both functions ingest project-controlled file content that workflow
// authors can shape deliberately, so adversarial / malformed input is
// in-scope. The acceptance contract is the same as the existing fuzz
// suite: never panic for any input. Errors are valid outputs; panics
// are bugs.
//
// Run a quick fuzz pass with:
//
//	go test -run='^FuzzParsePaneRoster|^FuzzExtractRosterYAMLBlock' ./internal/pipeline/...
//	go test -fuzz=FuzzParsePaneRosterYAML -fuzztime=30s ./internal/pipeline/...
//	go test -fuzz=FuzzExtractRosterYAMLBlock -fuzztime=30s ./internal/pipeline/...

import (
	"strings"
	"testing"
)

// ----------------------------------------------------------------------------
// FuzzParsePaneRosterYAML
// ----------------------------------------------------------------------------

// fuzzPaneRosterYAMLSeeds covers the documented roster shapes plus
// representative malformed cases. The parser tries three top-level
// shapes (panes:, roster:, raw list) so seeds exercise each path.
var fuzzPaneRosterYAMLSeeds = []string{
	// Canonical structured-list form under panes:
	`panes:
  - pane: 1
    role: investigator
    model: gpt-5
    domain: [H-001, H-005]
    productive_ignorance: true
  - pane: 2
    role: reviewer
    model: claude-opus
    domain: H-010
`,
	// Same payload under the alias key roster:
	`roster:
  - pane: 1
    role: investigator
    model: gpt-5
    domain: [H-001, H-005]
`,
	// Raw top-level list form (no panes:/roster: key).
	`- pane: 3
  role: oncall
  model: sonnet
  domain: H-099
- pane: 4
  role: scribe
  domain: [H-001]
`,
	// Empty document.
	``,
	// Whitespace only.
	"   \n\t  \n",
	// Pane index missing — entry should still parse (Index=0) without panicking.
	`panes:
  - role: ghost
    model: ""
    domain: []
`,
	// productive_ignorance present-but-false should keep the OK flag asserted.
	`panes:
  - pane: 7
    role: idle
    productive_ignorance: false
`,
	// Domain as a comma-separated string (the domainList scalar branch).
	`panes:
  - pane: 9
    role: cross-cutter
    domain: H-001,H-002,H-003
`,
	// Mixed scalar / sequence domains across entries.
	`panes:
  - pane: 1
    domain: H-001
  - pane: 2
    domain:
      - H-002
      - H-003
`,
	// Deeply nested object that the parser will reject — must not panic.
	`panes:
  - pane: 1
    domain:
      nested:
        deeper:
          - alpha
          - beta
`,
	// Wrong-typed pane index (string instead of int) — parser surfaces an
	// error; fuzz target accepts that as long as nothing panics.
	`panes:
  - pane: "not-an-int"
    role: typo
`,
	// Duplicated keys at the same level (yaml.v3 tolerates these but the
	// behavior shouldn't panic regardless of which value wins).
	`panes:
  - pane: 1
    role: first
    role: second
`,
	// Unicode payload in user-controlled fields.
	`panes:
  - pane: 1
    role: "🟢 investigator"
    model: "gpt-α"
    domain: ["H-π", "H-Ω"]
`,
	// Anchor / alias node — yaml.v3 supports these; parser must handle
	// the resulting interface{} without panicking.
	`anchor: &shared
  role: shared-role
  model: shared-model
panes:
  - pane: 1
    <<: *shared
`,
	// Truncated document mid-key.
	`panes:
  - pane: 1
    role:`,
	// Extremely long scalar to stress the YAML decoder.
	`panes:
  - pane: 1
    role: ` + strings.Repeat("x", 4096) + "\n",
	// Tab-indented (YAML rejects tabs in indentation; parser must error
	// cleanly instead of panicking).
	"panes:\n\t- pane: 1\n\t  role: tabby\n",
	// Document marker terminator, twice — multi-document streams.
	`panes:
  - pane: 1
    role: a
---
panes:
  - pane: 2
    role: b
`,
	// Just punctuation.
	`{}`,
	`[]`,
	`!!null`,
	// Pure-tag attack (yaml.v3 typically ignores unknown tags).
	`!!set
panes: !!seq
  - pane: !!int 1
    role: !!str investigator
`,
}

// FuzzParsePaneRosterYAML drives parsePaneRosterYAML with arbitrary bytes
// and asserts neither the YAML decoder nor the post-decode normalization
// loop panics. Errors are expected and ignored; entries returned with
// err==nil are sanity-checked for invariants (Source field set, slice
// fields safely iterable) so the fuzzer can also catch silent
// nil-deref-prone outputs.
func FuzzParsePaneRosterYAML(f *testing.F) {
	for _, seed := range fuzzPaneRosterYAMLSeeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// Cap the input length so a runaway fuzzer can't produce
		// pathological YAML that runs the decoder for minutes per call —
		// the bug class we're hunting is panics, not algorithmic
		// complexity attacks (those belong in a separate benchmark).
		if len(data) > 64*1024 {
			t.Skip("input too large for fuzz iteration budget")
		}
		entries, err := parsePaneRosterYAML(data, "fuzz")
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.Source != "fuzz" {
				t.Fatalf("Source = %q, want %q", entry.Source, "fuzz")
			}
			// Iterating Domains exercises slice-typed unmarshal paths;
			// the body is empty because we only care that the slice is
			// well-formed (range never panics on a properly-typed nil).
			for range entry.Domains {
			}
		}
	})
}

// ----------------------------------------------------------------------------
// FuzzExtractRosterYAMLBlock
// ----------------------------------------------------------------------------

// fuzzExtractRosterSeeds covers the markdown shapes RESUME.md and
// phase0_scope_decision.md may produce: header at top, header in the
// middle, fenced YAML block, unfenced YAML body, multiple sections, etc.
var fuzzExtractRosterSeeds = []string{
	// Canonical fenced-code-block shape used by RESUME.md.
	"## Roster\n\n```yaml\npanes:\n  - pane: 1\n    role: a\n```\n\n## Other\nfollow-up notes\n",
	// Canonical unfenced shape used by phase0_scope_decision.md.
	"## Roster\npanes:\n  - pane: 1\n    role: a\n  - pane: 2\n    role: b\n\n## Context\nhuman notes\n",
	// Roster header further down the document.
	"# Project notes\n\nIntro paragraph.\n\n## Roster\n```\npanes:\n  - pane: 1\n```\n",
	// No Roster section at all — extractor returns "".
	"# Title\n## Other\nbody\n",
	// Roster section that is empty.
	"## Roster\n\n## Other\nfollow-up\n",
	// Two ## Roster sections — extractor stops at first follow-on header.
	"## Roster\nfirst:body\n## Roster\nsecond:body\n",
	// Fence with language hint and trailing whitespace on the marker line.
	"## Roster\n```yaml   \npanes: []\n```   \n",
	// Mismatched fence (open without close) — extractor must not panic
	// and should still return a sensible string (possibly the partial
	// block or empty).
	"## Roster\n```yaml\npanes:\n  - pane: 1\n",
	// Fence opens after some unfenced content — extractor preserves the
	// original ordering, but importantly never panics.
	"## Roster\nintro line\n```\nfenced: true\n```\nepilogue\n",
	// Window-line-endings.
	"## Roster\r\n```\r\npanes: []\r\n```\r\n",
	// Roster header in mixed case (## roster) — extractor uses
	// EqualFold so it should be picked up.
	"## roster\n```\npanes: []\n```\n",
	// Header line with trailing markdown attributes.
	"## Roster {.no-toc}\n```\npanes: []\n```\n",
	// Empty input.
	"",
	// Just header.
	"## Roster\n",
	// Just fence markers, no content.
	"## Roster\n```\n```\n",
	// Long unicode body inside Roster section.
	"## Roster\npanes:\n  - role: 🛠️\n    model: gpt-α\n",
	// Pathological deeply-nested header levels.
	"###### Roster\n```\npanes: []\n```\n",
	// Roster as ## but with different line separator.
	"## Roster\n```yaml\npanes:\n  - pane: 1\n```",
}

// FuzzExtractRosterYAMLBlock drives extractRosterYAMLBlock with arbitrary
// strings. The function performs string scanning (split-by-newline, fence
// state machine) so the bug class we're guarding against is index-out-of-
// bounds / off-by-one panics on edge-case markdown. The output must be a
// well-formed string that's safe to feed into parsePaneRosterYAML, so
// this fuzz target also chains the two parsers — any block extractor
// output that crashes the YAML parser is itself a bug.
func FuzzExtractRosterYAMLBlock(f *testing.F) {
	for _, seed := range fuzzExtractRosterSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		if len(content) > 64*1024 {
			t.Skip("input too large for fuzz iteration budget")
		}
		block := extractRosterYAMLBlock(content)
		// Round-trip into the YAML parser to catch combined-pipeline
		// panics. The YAML parser may legitimately return an error;
		// only panics are bugs.
		_, _ = parsePaneRosterYAML([]byte(block), "fuzz")
	})
}
