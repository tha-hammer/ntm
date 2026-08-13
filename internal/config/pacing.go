package config

import (
	"fmt"
	"time"
)

// SpawnPacingConfig configures the global spawn scheduler pacing behavior.
// This exposes user-configurable settings for rate limiting, concurrency caps,
// and resource guardrails during agent/pane creation.
type SpawnPacingConfig struct {
	// Enabled controls whether spawn pacing/scheduling is active.
	// When disabled, spawns happen immediately without rate limiting.
	Enabled bool `toml:"enabled"`

	// MaxConcurrentSpawns is the maximum number of concurrent spawn operations.
	// Higher values increase parallelism but may cause resource contention.
	MaxConcurrentSpawns int `toml:"max_concurrent_spawns"`

	// MaxSpawnsPerSecond is the global spawn rate limit (tokens per second).
	// This controls how quickly agents can be spawned across all sessions.
	MaxSpawnsPerSecond float64 `toml:"max_spawns_per_sec"`

	// BurstSize is the maximum burst of spawns allowed before rate limiting kicks in.
	// Higher values allow more spawns in quick succession.
	BurstSize int `toml:"burst_size"`

	// DefaultRetries is the default number of retry attempts for failed spawns.
	DefaultRetries int `toml:"default_retries"`

	// RetryDelayMs is the default delay between retry attempts in milliseconds.
	RetryDelayMs int `toml:"retry_delay_ms"`

	// BackpressureThreshold is the queue size that triggers backpressure alerts.
	// When the queue exceeds this size, warnings are emitted.
	BackpressureThreshold int `toml:"backpressure_threshold"`

	// AgentCaps contains per-agent-type concurrency caps and pacing.
	AgentCaps AgentPacingConfig `toml:"agent_caps"`

	// Headroom contains resource headroom configuration.
	Headroom HeadroomPacingConfig `toml:"headroom"`

	// Backoff contains backoff configuration for resource errors.
	Backoff BackoffPacingConfig `toml:"backoff"`
}

// AgentPacingConfig holds per-agent-type concurrency and rate limiting settings.
type AgentPacingConfig struct {
	// Claude/cc agent settings
	ClaudeMaxConcurrent int     `toml:"claude_max_concurrent"`   // Max concurrent claude spawns
	ClaudeRatePerSec    float64 `toml:"claude_rate_per_sec"`     // Claude spawn rate limit
	ClaudeRampUpDelayMs int     `toml:"claude_ramp_up_delay_ms"` // Delay before full rate (warm-up)

	// Codex/cod agent settings
	CodexMaxConcurrent int     `toml:"codex_max_concurrent"`   // Max concurrent codex spawns
	CodexRatePerSec    float64 `toml:"codex_rate_per_sec"`     // Codex spawn rate limit
	CodexRampUpDelayMs int     `toml:"codex_ramp_up_delay_ms"` // Delay before full rate

	// Gemini/gmi agent settings
	GeminiMaxConcurrent int     `toml:"gemini_max_concurrent"`   // Max concurrent gemini spawns
	GeminiRatePerSec    float64 `toml:"gemini_rate_per_sec"`     // Gemini spawn rate limit
	GeminiRampUpDelayMs int     `toml:"gemini_ramp_up_delay_ms"` // Delay before full rate

	// CooldownOnFailureMs is the per-agent cooldown when a spawn fails.
	CooldownOnFailureMs int `toml:"cooldown_on_failure_ms"`

	// RecoverySuccesses is how many successes needed to restore full capacity after cooldown.
	RecoverySuccesses int `toml:"recovery_successes"`
}

// HeadroomPacingConfig configures pre-spawn resource headroom checks.
type HeadroomPacingConfig struct {
	// Enabled controls whether headroom checking is active.
	Enabled bool `toml:"enabled"`

	// MinFreeMB is the minimum free memory required to spawn (megabytes).
	MinFreeMB int `toml:"min_free_mb"`

	// MinFreeDiskMB is the minimum free disk space required (megabytes).
	MinFreeDiskMB int `toml:"min_free_disk_mb"`

	// MaxLoadAverage is the maximum 1-minute load average before blocking spawns.
	MaxLoadAverage float64 `toml:"max_load_average"`

	// MaxOpenFiles is the maximum number of open file descriptors before blocking.
	MaxOpenFiles int `toml:"max_open_files"`

	// CheckIntervalMs is the interval between resource checks in milliseconds.
	CheckIntervalMs int `toml:"check_interval_ms"`

	// PerAgentExpectedMemMB is the assumed typical (not worst-case) memory
	// footprint of one freshly spawned agent, in megabytes, used to size the
	// aggregate-memory spawn admission check (requested agents * this value,
	// vs live MemAvailable). Deliberately independent from and far below
	// PerAgentMemLimitMB (the systemd MemoryMax hard cap): the cap exists to
	// bound a single misbehaving agent, not to describe what agents normally
	// use. Zero means "use the built-in default" (see
	// config.PerAgentExpectedMemMB).
	PerAgentExpectedMemMB int `toml:"per_agent_expected_mem_mb"`
}

// BackoffPacingConfig configures exponential backoff for resource errors.
type BackoffPacingConfig struct {
	// InitialDelayMs is the initial backoff delay in milliseconds.
	InitialDelayMs int `toml:"initial_delay_ms"`

	// MaxDelayMs is the maximum backoff delay in milliseconds.
	MaxDelayMs int `toml:"max_delay_ms"`

	// Multiplier is the backoff multiplier (typically 2.0 for exponential).
	Multiplier float64 `toml:"multiplier"`

	// MaxConsecutiveFailures triggers global pause after this many failures.
	MaxConsecutiveFailures int `toml:"max_consecutive_failures"`

	// GlobalPauseDurationMs is the global pause duration after max failures.
	GlobalPauseDurationMs int `toml:"global_pause_duration_ms"`
}

// DefaultSpawnPacingConfig returns sensible spawn pacing defaults.
func DefaultSpawnPacingConfig() SpawnPacingConfig {
	return SpawnPacingConfig{
		Enabled:               true, // Enabled by default for safety
		MaxConcurrentSpawns:   4,
		MaxSpawnsPerSecond:    2.0,
		BurstSize:             5,
		DefaultRetries:        3,
		RetryDelayMs:          1000,
		BackpressureThreshold: 50,
		AgentCaps: AgentPacingConfig{
			ClaudeMaxConcurrent: 3,
			ClaudeRatePerSec:    1.5,
			ClaudeRampUpDelayMs: 0,
			CodexMaxConcurrent:  2,
			CodexRatePerSec:     0.5, // Codex has stricter rate limits
			CodexRampUpDelayMs:  1000,
			GeminiMaxConcurrent: 2,
			GeminiRatePerSec:    1.0,
			GeminiRampUpDelayMs: 0,
			CooldownOnFailureMs: 5000,
			RecoverySuccesses:   3,
		},
		Headroom: HeadroomPacingConfig{
			Enabled:         true,
			MinFreeMB:       512,   // 512 MB minimum free memory
			MinFreeDiskMB:   1024,  // 1 GB minimum free disk
			MaxLoadAverage:  8.0,   // Max load average
			MaxOpenFiles:    50000, // Max open file descriptors
			CheckIntervalMs: 5000,  // Check every 5 seconds
		},
		Backoff: BackoffPacingConfig{
			InitialDelayMs:         1000,  // 1 second initial
			MaxDelayMs:             60000, // 1 minute max
			Multiplier:             2.0,
			MaxConsecutiveFailures: 5,
			GlobalPauseDurationMs:  30000, // 30 second pause
		},
	}
}

// ValidateSpawnPacingConfig validates the spawn pacing configuration.
func ValidateSpawnPacingConfig(cfg *SpawnPacingConfig) error {
	if !cfg.Enabled {
		// Skip validation if pacing is disabled
		return nil
	}

	// Validate concurrency
	if cfg.MaxConcurrentSpawns < 1 {
		return fmt.Errorf("max_concurrent_spawns must be at least 1, got %d", cfg.MaxConcurrentSpawns)
	}

	// Validate rate limit
	if cfg.MaxSpawnsPerSecond <= 0 {
		return fmt.Errorf("max_spawns_per_sec must be positive, got %f", cfg.MaxSpawnsPerSecond)
	}

	// Validate burst size
	if cfg.BurstSize < 1 {
		return fmt.Errorf("burst_size must be at least 1, got %d", cfg.BurstSize)
	}

	// Validate retries
	if cfg.DefaultRetries < 0 {
		return fmt.Errorf("default_retries must be non-negative, got %d", cfg.DefaultRetries)
	}

	// Validate retry delay
	if cfg.RetryDelayMs < 0 {
		return fmt.Errorf("retry_delay_ms must be non-negative, got %d", cfg.RetryDelayMs)
	}

	// Validate backpressure threshold
	if cfg.BackpressureThreshold < 1 {
		return fmt.Errorf("backpressure_threshold must be at least 1, got %d", cfg.BackpressureThreshold)
	}

	// Validate agent caps
	if err := validateAgentPacingConfig(&cfg.AgentCaps); err != nil {
		return fmt.Errorf("agent_caps: %w", err)
	}

	// Validate headroom
	if cfg.Headroom.Enabled {
		if err := validateHeadroomPacingConfig(&cfg.Headroom); err != nil {
			return fmt.Errorf("headroom: %w", err)
		}
	}

	// Validate backoff
	if err := validateBackoffPacingConfig(&cfg.Backoff); err != nil {
		return fmt.Errorf("backoff: %w", err)
	}

	return nil
}

// validateAgentPacingConfig validates agent-specific pacing settings.
func validateAgentPacingConfig(cfg *AgentPacingConfig) error {
	// Claude
	if cfg.ClaudeMaxConcurrent < 0 {
		return fmt.Errorf("claude_max_concurrent must be non-negative, got %d", cfg.ClaudeMaxConcurrent)
	}
	if cfg.ClaudeRatePerSec < 0 {
		return fmt.Errorf("claude_rate_per_sec must be non-negative, got %f", cfg.ClaudeRatePerSec)
	}
	if cfg.ClaudeRampUpDelayMs < 0 {
		return fmt.Errorf("claude_ramp_up_delay_ms must be non-negative, got %d", cfg.ClaudeRampUpDelayMs)
	}

	// Codex
	if cfg.CodexMaxConcurrent < 0 {
		return fmt.Errorf("codex_max_concurrent must be non-negative, got %d", cfg.CodexMaxConcurrent)
	}
	if cfg.CodexRatePerSec < 0 {
		return fmt.Errorf("codex_rate_per_sec must be non-negative, got %f", cfg.CodexRatePerSec)
	}
	if cfg.CodexRampUpDelayMs < 0 {
		return fmt.Errorf("codex_ramp_up_delay_ms must be non-negative, got %d", cfg.CodexRampUpDelayMs)
	}

	// Gemini
	if cfg.GeminiMaxConcurrent < 0 {
		return fmt.Errorf("gemini_max_concurrent must be non-negative, got %d", cfg.GeminiMaxConcurrent)
	}
	if cfg.GeminiRatePerSec < 0 {
		return fmt.Errorf("gemini_rate_per_sec must be non-negative, got %f", cfg.GeminiRatePerSec)
	}
	if cfg.GeminiRampUpDelayMs < 0 {
		return fmt.Errorf("gemini_ramp_up_delay_ms must be non-negative, got %d", cfg.GeminiRampUpDelayMs)
	}

	// Cooldown and recovery
	if cfg.CooldownOnFailureMs < 0 {
		return fmt.Errorf("cooldown_on_failure_ms must be non-negative, got %d", cfg.CooldownOnFailureMs)
	}
	if cfg.RecoverySuccesses < 0 {
		return fmt.Errorf("recovery_successes must be non-negative, got %d", cfg.RecoverySuccesses)
	}

	return nil
}

// validateHeadroomPacingConfig validates headroom settings.
func validateHeadroomPacingConfig(cfg *HeadroomPacingConfig) error {
	if cfg.MinFreeMB < 0 {
		return fmt.Errorf("min_free_mb must be non-negative, got %d", cfg.MinFreeMB)
	}
	if cfg.MinFreeDiskMB < 0 {
		return fmt.Errorf("min_free_disk_mb must be non-negative, got %d", cfg.MinFreeDiskMB)
	}
	if cfg.MaxLoadAverage < 0 {
		return fmt.Errorf("max_load_average must be non-negative, got %f", cfg.MaxLoadAverage)
	}
	if cfg.MaxOpenFiles < 0 {
		return fmt.Errorf("max_open_files must be non-negative, got %d", cfg.MaxOpenFiles)
	}
	if cfg.PerAgentExpectedMemMB < 0 {
		return fmt.Errorf("per_agent_expected_mem_mb must be non-negative, got %d", cfg.PerAgentExpectedMemMB)
	}
	if cfg.CheckIntervalMs < 100 {
		return fmt.Errorf("check_interval_ms must be at least 100, got %d", cfg.CheckIntervalMs)
	}
	return nil
}

// validateBackoffPacingConfig validates backoff settings.
func validateBackoffPacingConfig(cfg *BackoffPacingConfig) error {
	if cfg.InitialDelayMs < 0 {
		return fmt.Errorf("initial_delay_ms must be non-negative, got %d", cfg.InitialDelayMs)
	}
	if cfg.MaxDelayMs < cfg.InitialDelayMs {
		return fmt.Errorf("max_delay_ms (%d) must be >= initial_delay_ms (%d)",
			cfg.MaxDelayMs, cfg.InitialDelayMs)
	}
	if cfg.Multiplier < 1.0 {
		return fmt.Errorf("multiplier must be at least 1.0, got %f", cfg.Multiplier)
	}
	if cfg.MaxConsecutiveFailures < 1 {
		return fmt.Errorf("max_consecutive_failures must be at least 1, got %d", cfg.MaxConsecutiveFailures)
	}
	if cfg.GlobalPauseDurationMs < 0 {
		return fmt.Errorf("global_pause_duration_ms must be non-negative, got %d", cfg.GlobalPauseDurationMs)
	}
	return nil
}

// RetryDelay returns the retry delay as a time.Duration.
func (c *SpawnPacingConfig) RetryDelay() time.Duration {
	return time.Duration(c.RetryDelayMs) * time.Millisecond
}

// ClaudeRampUpDelay returns the Claude ramp-up delay as a time.Duration.
func (c *AgentPacingConfig) ClaudeRampUpDelay() time.Duration {
	return time.Duration(c.ClaudeRampUpDelayMs) * time.Millisecond
}

// CodexRampUpDelay returns the Codex ramp-up delay as a time.Duration.
func (c *AgentPacingConfig) CodexRampUpDelay() time.Duration {
	return time.Duration(c.CodexRampUpDelayMs) * time.Millisecond
}

// GeminiRampUpDelay returns the Gemini ramp-up delay as a time.Duration.
func (c *AgentPacingConfig) GeminiRampUpDelay() time.Duration {
	return time.Duration(c.GeminiRampUpDelayMs) * time.Millisecond
}

// CooldownOnFailure returns the cooldown duration as a time.Duration.
func (c *AgentPacingConfig) CooldownOnFailure() time.Duration {
	return time.Duration(c.CooldownOnFailureMs) * time.Millisecond
}

// InitialDelay returns the initial backoff delay as a time.Duration.
func (c *BackoffPacingConfig) InitialDelay() time.Duration {
	return time.Duration(c.InitialDelayMs) * time.Millisecond
}

// MaxDelay returns the max backoff delay as a time.Duration.
func (c *BackoffPacingConfig) MaxDelay() time.Duration {
	return time.Duration(c.MaxDelayMs) * time.Millisecond
}

// GlobalPauseDuration returns the global pause duration as a time.Duration.
func (c *BackoffPacingConfig) GlobalPauseDuration() time.Duration {
	return time.Duration(c.GlobalPauseDurationMs) * time.Millisecond
}

// CheckInterval returns the headroom check interval as a time.Duration.
func (c *HeadroomPacingConfig) CheckInterval() time.Duration {
	return time.Duration(c.CheckIntervalMs) * time.Millisecond
}
