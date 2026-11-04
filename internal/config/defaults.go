package config

// Defaults returns a fully populated configuration.
//
// Defaults are applied before the YAML file is decoded and before environment
// overrides are applied, so a partial config file only needs to state what it
// wants to change.
func Defaults() Config {
	return Config{
		App: App{
			Name:      "ccresu",
			LogLevel:  "info",
			LogFormat: "json",
			Timezone:  "UTC",
		},
		Claude: Claude{
			Executable:          "claude",
			DiscoveryInterval:   NewDuration(30e9),
			MonitoredSessions:   []string{},
			DiscoverAllSessions: true,
			ResumeEnabled:       true,
			ContinuationMessage: DefaultContinuationMessage,
		},
		Monitoring: Monitoring{
			Enabled:                 true,
			DetectUsageLimits:       true,
			DetectSessionCompletion: true,
		},
		Detector: Detector{
			Threshold:      DefaultDetectorThreshold,
			CustomPatterns: []DetectorPattern{},
		},
		Scheduler: Scheduler{
			PollInterval:                NewDuration(30e9),
			UnknownResetInitialInterval: NewDuration(5 * 60e9),
			UnknownResetMaxInterval:     NewDuration(30 * 60e9),
			RecheckBeforeResume:         true,
		},
		Recovery: Recovery{
			Enabled:                 true,
			MaxAttempts:             10,
			InitialDelay:            NewDuration(30e9),
			MaxDelay:                NewDuration(15 * 60e9),
			Multiplier:              2.0,
			Jitter:                  0.15,
			StartupTimeout:          NewDuration(60e9),
			VerificationTimeout:     NewDuration(120e9),
			HealthCheckInterval:     NewDuration(10e9),
			MaxConcurrentRecoveries: 3,
		},
		Telegram: Telegram{
			Enabled:                false,
			ParseMode:              "HTML",
			NotifyLimitDetected:    true,
			NotifyWaiting:          false,
			NotifyRecoveryStarted:  true,
			NotifyRecoverySuccess:  true,
			NotifyRecoveryFailure:  true,
			NotifyPermanentFailure: true,
			NotifySessionCompleted: true,
		},
		Server: Server{
			Enabled: true,
			Host:    "127.0.0.1",
			Port:    8787,
			AllowedOrigins: []string{
				"http://127.0.0.1:8787",
				"http://localhost:8787",
			},
			TokenPath: "",
		},
		Storage: Storage{
			Driver:      "sqlite",
			Path:        "~/.local/share/ccresu/ccresu.db",
			BusyTimeout: NewDuration(5e9),
		},
	}
}

// DefaultDetectorThreshold is the minimum detection confidence. It mirrors
// detector.DefaultThreshold; it is duplicated here so that the configuration
// package does not depend on the detector.
const DefaultDetectorThreshold = 0.5

// DefaultContinuationMessage is sent to Claude Code when a resumed session needs
// an explicit nudge to keep working on its existing goal.
const DefaultContinuationMessage = "Continue working on the existing active goal from where execution was interrupted by the usage limit. Review the current state, avoid repeating completed work, and continue until the goal is completed."
