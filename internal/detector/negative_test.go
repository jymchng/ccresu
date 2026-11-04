package detector

import "testing"

func TestClassificationHelpers(t *testing.T) {
	t.Parallel()

	all := []Classification{
		ClassificationNone, ClassificationUsageLimit, ClassificationAuthError,
		ClassificationInvalidKey, ClassificationNetworkError, ClassificationUserCancelled,
		ClassificationCompleted,
	}
	for _, c := range all {
		if !c.Valid() {
			t.Errorf("%q must be valid", c)
		}
		if c.String() != string(c) {
			t.Errorf("String() = %q, want %q", c.String(), string(c))
		}
		if got := c.IsUsageLimit(); got != (c == ClassificationUsageLimit) {
			t.Errorf("IsUsageLimit() = %v for %q", got, c)
		}
	}
	for _, c := range []Classification{"", "limit", "LIMIT"} {
		if c.Valid() {
			t.Errorf("%q must not be valid", c)
		}
	}
}

func TestBestNegative(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want Classification
	}{
		{"invalid api key first", "invalid api key", ClassificationInvalidKey},
		{"invalid api key reversed", "your api key is invalid", ClassificationInvalidKey},
		{"missing credential", "missing credentials for this request", ClassificationInvalidKey},
		{"expired bearer token", "expired bearer token", ClassificationInvalidKey},
		{"x-api-key rejection", "api error: 401 invalid x-api-key", ClassificationInvalidKey},
		{"credential variable", "set anthropic_api_key to continue", ClassificationInvalidKey},
		{"unauthorised", "unauthorized request", ClassificationAuthError},
		{"auth failed", "authentication failed", ClassificationAuthError},
		{"not logged in", "you are not logged in", ClassificationAuthError},
		{"please log in", "please log in to continue", ClassificationAuthError},
		{"permission denied", "permission denied", ClassificationAuthError},
		{"oauth expired", "oauth token expired", ClassificationAuthError},
		{"connection refused", "connection refused", ClassificationNetworkError},
		{"connection timed out", "connection timed out while contacting the api", ClassificationNetworkError},
		{"econnrefused", "fetch failed: econnrefused", ClassificationNetworkError},
		{"dns", "dns lookup failed", ClassificationNetworkError},
		{"socket hang up", "socket hang up", ClassificationNetworkError},
		{"bad gateway", "502 bad gateway", ClassificationNetworkError},
		{"service unavailable", "503 service unavailable", ClassificationNetworkError},
		{"gateway timeout", "504 gateway timeout", ClassificationNetworkError},
		{"cancelled by user", "operation cancelled by user", ClassificationUserCancelled},
		{"interrupted by user", "interrupted by user", ClassificationUserCancelled},
		{"request aborted", "request aborted", ClassificationUserCancelled},
		{"context canceled", "context canceled", ClassificationUserCancelled},
		{"user aborted", "user aborted the run", ClassificationUserCancelled},
		{"goal complete", "goal completed successfully", ClassificationCompleted},
		{"session ended", "session completed", ClassificationCompleted},
		{"task done", "task is done", ClassificationCompleted},
		{"all tasks finished", "all tasks finished", ClassificationCompleted},
		{"finished successfully", "finished successfully", ClassificationCompleted},
		{"no more work", "no more work to do", ClassificationCompleted},
		{"session closed normally", "session ended normally", ClassificationCompleted},
		{"ordinary text", "i updated the file and the tests pass", ClassificationNone},
		{"empty", "", ClassificationNone},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rule := bestNegative(tc.in)
			got := ClassificationNone
			if rule.re != nil {
				got = rule.Classification
			}
			if got != tc.want {
				t.Fatalf("bestNegative(%q) = %q (%q), want %q", tc.in, got, rule.Description, tc.want)
			}
		})
	}
}

func TestBestNegativePrefersSpecificity(t *testing.T) {
	t.Parallel()

	// Both an authentication failure and a bad credential are present; the
	// credential is the more specific and more actionable answer.
	rule := bestNegative("authentication failed: invalid api key")
	if rule.Classification != ClassificationInvalidKey {
		t.Fatalf("classification = %q, want invalid_key", rule.Classification)
	}
	if rule.Confidence <= 0 {
		t.Fatalf("confidence = %v, want a positive value", rule.Confidence)
	}
}

func TestNegativeRulesAreCompiled(t *testing.T) {
	t.Parallel()

	for _, rule := range negativeRules {
		if rule.re == nil {
			t.Fatalf("negative rule %q was not compiled", rule.Description)
		}
		if rule.Confidence <= 0 || rule.Confidence > 1 {
			t.Fatalf("negative rule %q has confidence %v", rule.Description, rule.Confidence)
		}
	}
}
