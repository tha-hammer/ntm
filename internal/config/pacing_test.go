package config

import (
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestDefaultSpawnPacingConfig(t *testing.T) {
	cfg := DefaultSpawnPacingConfig()

	// Verify defaults are sensible
	if !cfg.Enabled {
		t.Error("expected pacing to be enabled by default")
	}
	if cfg.MaxConcurrentSpawns != 4 {
		t.Errorf("expected max_concurrent_spawns=4, got %d", cfg.MaxConcurrentSpawns)
	}
	if cfg.MaxSpawnsPerSecond != 2.0 {
		t.Errorf("expected max_spawns_per_sec=2.0, got %f", cfg.MaxSpawnsPerSecond)
	}
	if cfg.BurstSize != 5 {
		t.Errorf("expected burst_size=5, got %d", cfg.BurstSize)
	}
	if cfg.DefaultRetries != 3 {
		t.Errorf("expected default_retries=3, got %d", cfg.DefaultRetries)
	}
	if cfg.BackpressureThreshold != 50 {
		t.Errorf("expected backpressure_threshold=50, got %d", cfg.BackpressureThreshold)
	}

	// Verify agent caps
	if cfg.AgentCaps.ClaudeMaxConcurrent != 3 {
		t.Errorf("expected claude_max_concurrent=3, got %d", cfg.AgentCaps.ClaudeMaxConcurrent)
	}
	if cfg.AgentCaps.CodexMaxConcurrent != 2 {
		t.Errorf("expected codex_max_concurrent=2, got %d", cfg.AgentCaps.CodexMaxConcurrent)
	}
	if cfg.AgentCaps.GeminiMaxConcurrent != 2 {
		t.Errorf("expected gemini_max_concurrent=2, got %d", cfg.AgentCaps.GeminiMaxConcurrent)
	}

	// Verify headroom defaults
	if !cfg.Headroom.Enabled {
		t.Error("expected headroom to be enabled by default")
	}
	if cfg.Headroom.MinFreeMB != 512 {
		t.Errorf("expected min_free_mb=512, got %d", cfg.Headroom.MinFreeMB)
	}
	if cfg.Headroom.MinFreeDiskMB != 1024 {
		t.Errorf("expected min_free_disk_mb=1024, got %d", cfg.Headroom.MinFreeDiskMB)
	}

	// Verify backoff defaults
	if cfg.Backoff.InitialDelayMs != 1000 {
		t.Errorf("expected initial_delay_ms=1000, got %d", cfg.Backoff.InitialDelayMs)
	}
	if cfg.Backoff.MaxDelayMs != 60000 {
		t.Errorf("expected max_delay_ms=60000, got %d", cfg.Backoff.MaxDelayMs)
	}
	if cfg.Backoff.Multiplier != 2.0 {
		t.Errorf("expected multiplier=2.0, got %f", cfg.Backoff.Multiplier)
	}
}

func TestValidateSpawnPacingConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SpawnPacingConfig
		wantErr bool
	}{
		{
			name:    "valid default config",
			cfg:     DefaultSpawnPacingConfig(),
			wantErr: false,
		},
		{
			name: "disabled config skips validation",
			cfg: SpawnPacingConfig{
				Enabled:             false,
				MaxConcurrentSpawns: -1, // Would be invalid if enabled
			},
			wantErr: false,
		},
		{
			name: "invalid max_concurrent_spawns",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.MaxConcurrentSpawns = 0
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "invalid max_spawns_per_sec",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.MaxSpawnsPerSecond = 0
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "negative max_spawns_per_sec",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.MaxSpawnsPerSecond = -1.0
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "invalid burst_size",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.BurstSize = 0
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "negative default_retries",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.DefaultRetries = -1
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "invalid backpressure_threshold",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.BackpressureThreshold = 0
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "negative claude_max_concurrent",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.AgentCaps.ClaudeMaxConcurrent = -1
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "negative codex_rate_per_sec",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.AgentCaps.CodexRatePerSec = -1.0
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "invalid headroom check_interval_ms",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.Headroom.CheckIntervalMs = 50 // Less than 100
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "max_delay_ms less than initial_delay_ms",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.Backoff.MaxDelayMs = 500
				cfg.Backoff.InitialDelayMs = 1000
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "multiplier less than 1.0",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.Backoff.Multiplier = 0.5
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "max_consecutive_failures less than 1",
			cfg: func() SpawnPacingConfig {
				cfg := DefaultSpawnPacingConfig()
				cfg.Backoff.MaxConsecutiveFailures = 0
				return cfg
			}(),
			wantErr: true,
		},
		{
			name: "valid custom config",
			cfg: SpawnPacingConfig{
				Enabled:               true,
				MaxConcurrentSpawns:   8,
				MaxSpawnsPerSecond:    5.0,
				BurstSize:             10,
				DefaultRetries:        5,
				RetryDelayMs:          2000,
				BackpressureThreshold: 100,
				AgentCaps: AgentPacingConfig{
					ClaudeMaxConcurrent: 5,
					ClaudeRatePerSec:    2.0,
					ClaudeRampUpDelayMs: 500,
					CodexMaxConcurrent:  3,
					CodexRatePerSec:     1.0,
					CodexRampUpDelayMs:  2000,
					GeminiMaxConcurrent: 3,
					GeminiRatePerSec:    1.5,
					GeminiRampUpDelayMs: 0,
					CooldownOnFailureMs: 10000,
					RecoverySuccesses:   5,
				},
				Headroom: HeadroomPacingConfig{
					Enabled:         true,
					MinFreeMB:       1024,
					MinFreeDiskMB:   2048,
					MaxLoadAverage:  16.0,
					MaxOpenFiles:    100000,
					CheckIntervalMs: 10000,
				},
				Backoff: BackoffPacingConfig{
					InitialDelayMs:         2000,
					MaxDelayMs:             120000,
					Multiplier:             2.5,
					MaxConsecutiveFailures: 10,
					GlobalPauseDurationMs:  60000,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSpawnPacingConfig(&tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSpawnPacingConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSpawnPacingDurationHelpers(t *testing.T) {
	cfg := DefaultSpawnPacingConfig()

	// Test RetryDelay
	if cfg.RetryDelay() != time.Second {
		t.Errorf("RetryDelay() = %v, want %v", cfg.RetryDelay(), time.Second)
	}

	// Test AgentCaps duration helpers
	if cfg.AgentCaps.CodexRampUpDelay() != time.Second {
		t.Errorf("CodexRampUpDelay() = %v, want %v", cfg.AgentCaps.CodexRampUpDelay(), time.Second)
	}
	if cfg.AgentCaps.CooldownOnFailure() != 5*time.Second {
		t.Errorf("CooldownOnFailure() = %v, want %v", cfg.AgentCaps.CooldownOnFailure(), 5*time.Second)
	}

	// Test Headroom duration helpers
	if cfg.Headroom.CheckInterval() != 5*time.Second {
		t.Errorf("CheckInterval() = %v, want %v", cfg.Headroom.CheckInterval(), 5*time.Second)
	}

	// Test Backoff duration helpers
	if cfg.Backoff.InitialDelay() != time.Second {
		t.Errorf("InitialDelay() = %v, want %v", cfg.Backoff.InitialDelay(), time.Second)
	}
	if cfg.Backoff.MaxDelay() != time.Minute {
		t.Errorf("MaxDelay() = %v, want %v", cfg.Backoff.MaxDelay(), time.Minute)
	}
	if cfg.Backoff.GlobalPauseDuration() != 30*time.Second {
		t.Errorf("GlobalPauseDuration() = %v, want %v", cfg.Backoff.GlobalPauseDuration(), 30*time.Second)
	}
}

func TestSpawnPacingConfigTOMLParsing(t *testing.T) {
	tomlContent := `
[spawn_pacing]
enabled = true
max_concurrent_spawns = 6
max_spawns_per_sec = 3.0
burst_size = 8
default_retries = 4
retry_delay_ms = 1500
backpressure_threshold = 75

[spawn_pacing.agent_caps]
claude_max_concurrent = 4
claude_rate_per_sec = 2.0
claude_ramp_up_delay_ms = 200
codex_max_concurrent = 3
codex_rate_per_sec = 0.8
codex_ramp_up_delay_ms = 1500
gemini_max_concurrent = 3
gemini_rate_per_sec = 1.2
gemini_ramp_up_delay_ms = 100
cooldown_on_failure_ms = 8000
recovery_successes = 4

[spawn_pacing.headroom]
enabled = true
min_free_mb = 768
min_free_disk_mb = 2048
max_load_average = 12.0
max_open_files = 75000
check_interval_ms = 3000

[spawn_pacing.backoff]
initial_delay_ms = 1500
max_delay_ms = 90000
multiplier = 2.2
max_consecutive_failures = 7
global_pause_duration_ms = 45000
`

	var cfg Config
	if _, err := toml.Decode(tomlContent, &cfg); err != nil {
		t.Fatalf("toml.Decode() error = %v", err)
	}

	// Verify parsed values
	if !cfg.SpawnPacing.Enabled {
		t.Error("expected enabled=true")
	}
	if cfg.SpawnPacing.MaxConcurrentSpawns != 6 {
		t.Errorf("expected max_concurrent_spawns=6, got %d", cfg.SpawnPacing.MaxConcurrentSpawns)
	}
	if cfg.SpawnPacing.MaxSpawnsPerSecond != 3.0 {
		t.Errorf("expected max_spawns_per_sec=3.0, got %f", cfg.SpawnPacing.MaxSpawnsPerSecond)
	}
	if cfg.SpawnPacing.BurstSize != 8 {
		t.Errorf("expected burst_size=8, got %d", cfg.SpawnPacing.BurstSize)
	}
	if cfg.SpawnPacing.DefaultRetries != 4 {
		t.Errorf("expected default_retries=4, got %d", cfg.SpawnPacing.DefaultRetries)
	}
	if cfg.SpawnPacing.BackpressureThreshold != 75 {
		t.Errorf("expected backpressure_threshold=75, got %d", cfg.SpawnPacing.BackpressureThreshold)
	}

	// Verify agent caps
	if cfg.SpawnPacing.AgentCaps.ClaudeMaxConcurrent != 4 {
		t.Errorf("expected claude_max_concurrent=4, got %d", cfg.SpawnPacing.AgentCaps.ClaudeMaxConcurrent)
	}
	if cfg.SpawnPacing.AgentCaps.CodexRatePerSec != 0.8 {
		t.Errorf("expected codex_rate_per_sec=0.8, got %f", cfg.SpawnPacing.AgentCaps.CodexRatePerSec)
	}
	if cfg.SpawnPacing.AgentCaps.CooldownOnFailureMs != 8000 {
		t.Errorf("expected cooldown_on_failure_ms=8000, got %d", cfg.SpawnPacing.AgentCaps.CooldownOnFailureMs)
	}

	// Verify headroom
	if cfg.SpawnPacing.Headroom.MinFreeMB != 768 {
		t.Errorf("expected min_free_mb=768, got %d", cfg.SpawnPacing.Headroom.MinFreeMB)
	}
	if cfg.SpawnPacing.Headroom.MaxLoadAverage != 12.0 {
		t.Errorf("expected max_load_average=12.0, got %f", cfg.SpawnPacing.Headroom.MaxLoadAverage)
	}

	// Verify backoff
	if cfg.SpawnPacing.Backoff.MaxDelayMs != 90000 {
		t.Errorf("expected max_delay_ms=90000, got %d", cfg.SpawnPacing.Backoff.MaxDelayMs)
	}
	if cfg.SpawnPacing.Backoff.Multiplier != 2.2 {
		t.Errorf("expected multiplier=2.2, got %f", cfg.SpawnPacing.Backoff.Multiplier)
	}
	if cfg.SpawnPacing.Backoff.GlobalPauseDurationMs != 45000 {
		t.Errorf("expected global_pause_duration_ms=45000, got %d", cfg.SpawnPacing.Backoff.GlobalPauseDurationMs)
	}

	// Verify validation passes
	if err := ValidateSpawnPacingConfig(&cfg.SpawnPacing); err != nil {
		t.Errorf("ValidateSpawnPacingConfig() error = %v", err)
	}
}

// =============================================================================
// AgentPacingConfig duration helpers
// =============================================================================

func TestAgentPacingConfig_RampUpDelays(t *testing.T) {
	t.Parallel()

	cfg := &AgentPacingConfig{
		ClaudeRampUpDelayMs: 5000,
		CodexRampUpDelayMs:  3000,
		GeminiRampUpDelayMs: 4000,
		CooldownOnFailureMs: 10000,
	}

	tests := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ClaudeRampUpDelay", cfg.ClaudeRampUpDelay(), 5 * time.Second},
		{"CodexRampUpDelay", cfg.CodexRampUpDelay(), 3 * time.Second},
		{"GeminiRampUpDelay", cfg.GeminiRampUpDelay(), 4 * time.Second},
		{"CooldownOnFailure", cfg.CooldownOnFailure(), 10 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestValidateAgentPacingConfig_AllNegativeBranches exercises every negative-value
// error branch in validateAgentPacingConfig individually.
func TestValidateAgentPacingConfig_AllNegativeBranches(t *testing.T) {
	t.Parallel()

	fields := []struct {
		name string
		set  func(*AgentPacingConfig)
	}{
		{"ClaudeMaxConcurrent", func(c *AgentPacingConfig) { c.ClaudeMaxConcurrent = -1 }},
		{"ClaudeRatePerSec", func(c *AgentPacingConfig) { c.ClaudeRatePerSec = -0.1 }},
		{"ClaudeRampUpDelayMs", func(c *AgentPacingConfig) { c.ClaudeRampUpDelayMs = -1 }},
		{"CodexMaxConcurrent", func(c *AgentPacingConfig) { c.CodexMaxConcurrent = -1 }},
		{"CodexRatePerSec", func(c *AgentPacingConfig) { c.CodexRatePerSec = -0.1 }},
		{"CodexRampUpDelayMs", func(c *AgentPacingConfig) { c.CodexRampUpDelayMs = -1 }},
		{"GeminiMaxConcurrent", func(c *AgentPacingConfig) { c.GeminiMaxConcurrent = -1 }},
		{"GeminiRatePerSec", func(c *AgentPacingConfig) { c.GeminiRatePerSec = -0.1 }},
		{"GeminiRampUpDelayMs", func(c *AgentPacingConfig) { c.GeminiRampUpDelayMs = -1 }},
		{"CooldownOnFailureMs", func(c *AgentPacingConfig) { c.CooldownOnFailureMs = -1 }},
		{"RecoverySuccesses", func(c *AgentPacingConfig) { c.RecoverySuccesses = -1 }},
	}

	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			cfg := &AgentPacingConfig{} // all zeros = valid
			f.set(cfg)
			err := validateAgentPacingConfig(cfg)
			if err == nil {
				t.Errorf("expected error for negative %s, got nil", f.name)
			}
		})
	}

	// All zeros should pass
	t.Run("all zeros valid", func(t *testing.T) {
		t.Parallel()
		cfg := &AgentPacingConfig{}
		if err := validateAgentPacingConfig(cfg); err != nil {
			t.Errorf("all-zero config should be valid, got: %v", err)
		}
	})
}

// TestValidateHeadroomPacingConfig_AllBranches exercises every error branch
// in validateHeadroomPacingConfig individually.
func TestValidateHeadroomPacingConfig_AllBranches(t *testing.T) {
	t.Parallel()

	validCfg := func() HeadroomPacingConfig {
		return HeadroomPacingConfig{
			MinFreeMB:       512,
			MinFreeDiskMB:   1024,
			MaxLoadAverage:  8.0,
			MaxOpenFiles:    50000,
			CheckIntervalMs: 5000,
		}
	}

	tests := []struct {
		name    string
		modify  func(*HeadroomPacingConfig)
		wantErr bool
	}{
		{"valid config", func(c *HeadroomPacingConfig) {}, false},
		{"MinFreeMB negative", func(c *HeadroomPacingConfig) { c.MinFreeMB = -1 }, true},
		{"MinFreeDiskMB negative", func(c *HeadroomPacingConfig) { c.MinFreeDiskMB = -1 }, true},
		{"MaxLoadAverage negative", func(c *HeadroomPacingConfig) { c.MaxLoadAverage = -0.1 }, true},
		{"MaxOpenFiles negative", func(c *HeadroomPacingConfig) { c.MaxOpenFiles = -1 }, true},
		{"CheckIntervalMs below 100", func(c *HeadroomPacingConfig) { c.CheckIntervalMs = 99 }, true},
		{"CheckIntervalMs exactly 100", func(c *HeadroomPacingConfig) { c.CheckIntervalMs = 100 }, false},
		{"CheckIntervalMs zero", func(c *HeadroomPacingConfig) { c.CheckIntervalMs = 0 }, true},
		{"PerAgentExpectedMemMB negative", func(c *HeadroomPacingConfig) { c.PerAgentExpectedMemMB = -1 }, true},
		{"PerAgentExpectedMemMB zero (unset, use default)", func(c *HeadroomPacingConfig) { c.PerAgentExpectedMemMB = 0 }, false},
		{"PerAgentExpectedMemMB positive override", func(c *HeadroomPacingConfig) { c.PerAgentExpectedMemMB = 4096 }, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validCfg()
			tc.modify(&cfg)
			err := validateHeadroomPacingConfig(&cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateHeadroomPacingConfig() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestAgentPacingConfig_ZeroDelays(t *testing.T) {
	t.Parallel()

	cfg := &AgentPacingConfig{}
	if cfg.ClaudeRampUpDelay() != 0 {
		t.Error("zero ClaudeRampUpDelayMs should yield 0 duration")
	}
	if cfg.CodexRampUpDelay() != 0 {
		t.Error("zero CodexRampUpDelayMs should yield 0 duration")
	}
	if cfg.GeminiRampUpDelay() != 0 {
		t.Error("zero GeminiRampUpDelayMs should yield 0 duration")
	}
	if cfg.CooldownOnFailure() != 0 {
		t.Error("zero CooldownOnFailureMs should yield 0 duration")
	}
}

// =============================================================================
// validateBackoffPacingConfig (bd-4b4zf)
// =============================================================================

func TestValidateBackoffPacingConfig_AllBranches(t *testing.T) {
	t.Parallel()

	validCfg := func() BackoffPacingConfig {
		return BackoffPacingConfig{
			InitialDelayMs:         1000,
			MaxDelayMs:             30000,
			Multiplier:             2.0,
			MaxConsecutiveFailures: 5,
			GlobalPauseDurationMs:  10000,
		}
	}

	tests := []struct {
		name    string
		modify  func(*BackoffPacingConfig)
		wantErr bool
	}{
		{"valid config", func(c *BackoffPacingConfig) {}, false},
		{"InitialDelayMs negative", func(c *BackoffPacingConfig) { c.InitialDelayMs = -1 }, true},
		{"MaxDelayMs less than InitialDelayMs", func(c *BackoffPacingConfig) { c.MaxDelayMs = 500 }, true},
		{"Multiplier below 1.0", func(c *BackoffPacingConfig) { c.Multiplier = 0.5 }, true},
		{"MaxConsecutiveFailures zero", func(c *BackoffPacingConfig) { c.MaxConsecutiveFailures = 0 }, true},
		{"GlobalPauseDurationMs negative", func(c *BackoffPacingConfig) { c.GlobalPauseDurationMs = -1 }, true},
		{"GlobalPauseDurationMs zero valid", func(c *BackoffPacingConfig) { c.GlobalPauseDurationMs = 0 }, false},
		{"InitialDelayMs zero valid", func(c *BackoffPacingConfig) { c.InitialDelayMs = 0; c.MaxDelayMs = 0 }, false},
		{"Multiplier exactly 1.0 valid", func(c *BackoffPacingConfig) { c.Multiplier = 1.0 }, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validCfg()
			tc.modify(&cfg)
			err := validateBackoffPacingConfig(&cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateBackoffPacingConfig() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
