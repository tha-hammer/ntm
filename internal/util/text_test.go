package util

import (
	"strings"
	"testing"
)

func TestExtractNewOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		before string
		after  string
		want   string
	}{
		{
			name:   "empty before returns all after",
			before: "",
			after:  "hello world",
			want:   "hello world",
		},
		{
			name:   "empty after returns empty",
			before: "hello",
			after:  "",
			want:   "",
		},
		{
			name:   "both empty",
			before: "",
			after:  "",
			want:   "",
		},
		{
			name:   "simple append",
			before: "hello",
			after:  "hello world",
			want:   " world",
		},
		{
			name:   "exact match returns empty",
			before: "hello",
			after:  "hello",
			want:   "",
		},
		{
			name:   "scrolled output with overlap",
			before: "line1\nline2\nline3",
			after:  "line2\nline3\nline4",
			want:   "\nline4",
		},
		{
			name:   "no overlap returns all",
			before: "abc",
			after:  "xyz",
			want:   "xyz",
		},
		{
			name:   "partial overlap at end",
			before: "abcdef",
			after:  "defghi",
			want:   "ghi",
		},
		{
			name:   "multiline overlap",
			before: "first\nsecond\nthird",
			after:  "second\nthird\nfourth",
			want:   "\nfourth",
		},
		{
			name:   "before longer than after with overlap",
			before: "this is a very long before string that ends with: overlap",
			after:  "overlap and new content",
			want:   " and new content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractNewOutput(tt.before, tt.after)
			if got != tt.want {
				t.Errorf("ExtractNewOutput(%q, %q) = %q, want %q", tt.before, tt.after, got, tt.want)
			}
		})
	}
}

func TestExtractNewOutput_LargeOverlap(t *testing.T) {
	t.Parallel()

	// Test with overlap larger than chunkSize (40)
	overlap := strings.Repeat("x", 50)
	before := "prefix" + overlap
	after := overlap + "suffix"

	got := ExtractNewOutput(before, after)
	want := "suffix"

	if got != want {
		t.Errorf("ExtractNewOutput with 50-char overlap = %q, want %q", got, want)
	}
}

func TestExtractNewOutput_SmallOverlap(t *testing.T) {
	t.Parallel()

	// Test with overlap smaller than chunkSize but after > chunkSize
	overlap := "xyz"                           // 3 char overlap
	before := "prefix" + overlap               // "prefixyz" (8 chars, ends with "xyz")
	after := overlap + strings.Repeat("b", 47) // "xyz" + 47 b's = 50 chars, starts with "xyz"

	got := ExtractNewOutput(before, after)
	want := strings.Repeat("b", 47)

	if got != want {
		t.Errorf("ExtractNewOutput with 3-char overlap, long after = %q, want %q", got, want)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{"empty string", "", 10, ""},
		{"n is zero", "hello", 0, ""},
		{"n is negative", "hello", -5, ""},
		{"string shorter than n", "hi", 10, "hi"},
		{"string equals n", "hello", 5, "hello"},
		{"truncate with ellipsis", "hello world", 8, "hello..."},
		{"truncate minimal ellipsis", "hello world", 5, "he..."},
		{"n too small for ellipsis", "hello", 2, "he"},
		{"n equals 3", "hello", 3, "hel"},
		{"single char n=1", "a", 1, "a"},
		{"multi-char n=1", "hello", 1, "h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Truncate(tt.input, tt.n)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
			}
		})
	}
}

func TestTruncate_UTF8(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{
			name:  "multibyte char that fits",
			input: "世界",
			n:     10,
			want:  "世界",
		},
		{
			name:  "multibyte truncated at boundary",
			input: "a世界",
			n:     4,
			want:  "a...",
		},
		{
			name:  "multibyte n too small",
			input: "世界",
			n:     2,
			want:  "", // First char is 3 bytes, can't fit in 2
		},
		{
			name:  "mixed ASCII and multibyte",
			input: "hi世界",
			n:     5,
			want:  "hi...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Truncate(tt.input, tt.n)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "hello", "hello"},
		{"with spaces", "hello world", "hello_world"},
		{"with slashes", "path/to/file", "path-to-file"},
		{"with backslashes", "path\\to\\file", "path-to-file"},
		{"with special chars", "file:name?.txt", "file-name-_txt"},
		{"with dots", "my.file.name", "my_file_name"},
		{"with leading space", "  trimmed  ", "trimmed"},
		{"empty string", "", ""},
		{"long string truncated", strings.Repeat("a", 100), strings.Repeat("a", 50)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestSanitizeFilename_UTF8Boundary tests the UTF-8 boundary truncation in SanitizeFilename.
func TestSanitizeFilename_UTF8Boundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"exactly 50 bytes no truncation",
			strings.Repeat("a", 50),
			strings.Repeat("a", 50),
		},
		{
			"51 bytes truncated to 50",
			strings.Repeat("a", 51),
			strings.Repeat("a", 50),
		},
		{
			"multibyte at boundary",
			// 48 ASCII 'a' chars + one 4-byte emoji = 52 bytes total after replacement
			// The emoji gets replaced by SanitizeFilename (not a special char, stays as-is)
			strings.Repeat("a", 48) + "\xf0\x9f\x8c\x8d", // 🌍 = 4 bytes
			strings.Repeat("a", 48),                      // truncated before the multibyte char
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeFilename(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeFilename(%q) = %q (len %d), want %q (len %d)",
					tc.input, got, len(got), tc.want, len(tc.want))
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			result := FormatBytes(tc.bytes)
			if result != tc.expected {
				t.Errorf("FormatBytes(%d) = %q, want %q", tc.bytes, result, tc.expected)
			}
		})
	}
}

func TestSafeSlice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncate ascii", "hello world", 5, "hello"},
		{"empty string", "", 5, ""},
		{"zero maxlen", "hello", 0, ""},
		// Multi-byte rune: "日" is 3 bytes
		{"rune boundary safe", "日本語", 4, "日"},
		{"rune boundary exact", "日本語", 6, "日本"},
		{"all multibyte fits", "日本語", 9, "日本語"},
		{"mixed cuts mid-rune", "a日b", 3, "a"}, // "日" needs bytes 1-3, s[:1]="a"
		{"mixed fits rune", "a日b", 4, "a日"},    // "日" ends at byte 4, s[:4]="a日"
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := SafeSlice(tc.input, tc.maxLen)
			if result != tc.expected {
				t.Errorf("SafeSlice(%q, %d) = %q, want %q", tc.input, tc.maxLen, result, tc.expected)
			}
		})
	}
}

// bd-itfik: GetLastNLines must match GNU `tail -n` semantics — a single
// trailing newline is the EOL of the last line, not the start of an
// (N+1)-th empty line. Pre-fix the reverse scan counted the trailing
// '\n' as the first separator and returned N-1 actual lines.
func TestGetLastNLines_TrailingNewlineDoesNotShrinkWindow(t *testing.T) {
	tests := []struct {
		name string
		text string
		n    int
		want string
	}{
		// Trailing-newline cases — these were broken pre-fix.
		{"two lines, trailing nl, n=2", "L1\nL2\n", 2, "L1\nL2\n"},
		{"three lines, trailing nl, n=2", "L1\nL2\nL3\n", 2, "L2\nL3\n"},
		{"single line, trailing nl, n=1", "abc\n", 1, "abc\n"},
		{"single line, trailing nl, n=2 (more than available)", "abc\n", 2, "abc\n"},

		// No-trailing-newline cases — already worked, pin them.
		{"two lines, no trailing nl, n=1", "L1\nL2", 1, "L2"},
		{"two lines, no trailing nl, n=2", "L1\nL2", 2, "L1\nL2"},
		{"single line, no trailing nl, n=1", "abc", 1, "abc"},

		// Empty text and zero-n.
		{"empty text", "", 1, ""},
		{"zero n", "anything\n", 0, ""},

		// Multi-trailing newlines: the inner '\n' marks an empty line.
		{"two trailing newlines, n=1", "L1\n\n", 1, "\n"},
		{"two trailing newlines, n=2", "L1\n\n", 2, "L1\n\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := GetLastNLines(tc.text, tc.n)
			if got != tc.want {
				t.Errorf("GetLastNLines(%q, %d) = %q, want %q", tc.text, tc.n, got, tc.want)
			}
		})
	}
}
