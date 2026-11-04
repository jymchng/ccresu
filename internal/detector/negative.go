package detector

import "regexp"

// Classification is the detector's verdict on a piece of session output.
//
// Only ClassificationUsageLimit means a usage limit was found. Every other
// value is an explicit non-classification: the message was recognised as
// something else, which matters because each one calls for different handling.
// An authentication failure must not be retried after a reset, an invalid API
// key must never be retried at all, and a completed session must never be
// resumed.
type Classification string

const (
	// ClassificationNone is ordinary output that matched nothing.
	ClassificationNone Classification = "none"
	// ClassificationUsageLimit is a confirmed usage-limit interruption.
	ClassificationUsageLimit Classification = "usage_limit"
	// ClassificationAuthError is an authentication or authorisation failure.
	ClassificationAuthError Classification = "auth_error"
	// ClassificationInvalidKey is a rejected, missing or expired credential.
	ClassificationInvalidKey Classification = "invalid_key"
	// ClassificationNetworkError is a transport failure.
	ClassificationNetworkError Classification = "network_error"
	// ClassificationUserCancelled is work the user interrupted.
	ClassificationUserCancelled Classification = "user_cancelled"
	// ClassificationCompleted is a session or goal that finished normally.
	ClassificationCompleted Classification = "completed"
)

// String renders the classification.
func (c Classification) String() string { return string(c) }

// Valid reports whether c is a known classification.
func (c Classification) Valid() bool {
	switch c {
	case ClassificationNone, ClassificationUsageLimit, ClassificationAuthError,
		ClassificationInvalidKey, ClassificationNetworkError, ClassificationUserCancelled,
		ClassificationCompleted:
		return true
	default:
		return false
	}
}

// IsUsageLimit reports whether the classification is a confirmed limit.
func (c Classification) IsUsageLimit() bool { return c == ClassificationUsageLimit }

// negativeRule is one pattern that proves a message is not a usage limit.
type negativeRule struct {
	Classification Classification
	Expression     string
	Confidence     float64
	Description    string

	re *regexp.Regexp
}

// negativeRules are ordered most-specific first so an invalid key is reported
// as an invalid key rather than as a generic authentication failure.
var negativeRules = []negativeRule{
	{
		Classification: ClassificationInvalidKey,
		Expression:     `\b(?:invalid|incorrect|missing|expired|revoked|malformed) (?:api[ _-]?key|x-api-key|auth(?:entication)? token|bearer token|access token|credentials?)\b`,
		Confidence:     0.95,
		Description:    "a rejected or missing credential",
	},
	{
		Classification: ClassificationInvalidKey,
		Expression:     `\b(?:api[ _-]?key|x-api-key|auth(?:entication)? token|bearer token|access token|credentials?)\b\s*(?:is\s+|was\s+|are\s+)?(?:invalid|incorrect|missing|expired|revoked|malformed|not set|not configured)\b`,
		Confidence:     0.95,
		Description:    "a credential described as invalid",
	},
	{
		Classification: ClassificationInvalidKey,
		Expression:     `\b(?:anthropic_api_key|openai_api_key|claude_api_key)\b`,
		Confidence:     0.9,
		Description:    "a credential variable named in an error",
	},
	{
		Classification: ClassificationNetworkError,
		Expression:     `\b(?:connection (?:refused|reset|closed|timed out|error)|network (?:error|unreachable|is down|failure)|econnrefused|econnreset|enotfound|etimedout|eai_again|socket hang up|dns (?:lookup|resolution) failed|fetch failed|could not resolve host|temporary failure in name resolution|tls handshake (?:error|timeout)|no route to host)\b`,
		Confidence:     0.92,
		Description:    "a transport-level failure",
	},
	{
		Classification: ClassificationNetworkError,
		Expression:     `\b(?:50[234] (?:bad gateway|service unavailable|gateway timeout)|bad gateway|service unavailable|gateway timeout)\b`,
		Confidence:     0.85,
		Description:    "an upstream HTTP failure",
	},
	{
		Classification: ClassificationAuthError,
		Expression:     `\b(?:unauthori[sz]ed|unauthenticated|authentication (?:failed|error|required|expired)|not (?:logged|signed) in|please (?:log|sign) ?in|oauth token (?:expired|revoked|invalid)|permission denied|access denied|forbidden|401 unauthorized)\b`,
		Confidence:     0.9,
		Description:    "an authentication or authorisation failure",
	},
	{
		Classification: ClassificationUserCancelled,
		Expression:     `\b(?:operation (?:was )?cancell?ed|(?:user|you) (?:cancell?ed|aborted|interrupted)|request aborted|context canceled|interrupted by user|esc(?:ape)? pressed|cancell?ed by (?:the )?user)\b`,
		Confidence:     0.9,
		Description:    "work the user interrupted",
	},
	{
		Classification: ClassificationCompleted,
		Expression:     `\b(?:goal|session|task|work|conversation) (?:is |was )?(?:complete|completed|done|finished)\b`,
		Confidence:     0.85,
		Description:    "a goal, session or task that finished normally",
	},
	{
		Classification: ClassificationCompleted,
		Expression:     `\b(?:all (?:work|tasks?|steps?) (?:is |are )?(?:complete|completed|done|finished)|finished successfully|completed successfully|no (?:more|remaining) (?:work|tasks?|steps?)|work (?:is )?complete)\b`,
		Confidence:     0.85,
		Description:    "a summary that reports normal completion",
	},
	{
		Classification: ClassificationCompleted,
		Expression:     `\b(?:session|conversation) (?:ended|closed) normally\b`,
		Confidence:     0.85,
		Description:    "a session that ended normally",
	},
}

func init() {
	for i := range negativeRules {
		negativeRules[i].re = regexp.MustCompile(negativeRules[i].Expression)
	}
}

// bestNegative returns the highest-confidence non-limit classification for the
// normalised text, or the zero rule when the text is not recognised.
func bestNegative(folded string) negativeRule {
	var best negativeRule
	for _, rule := range negativeRules {
		if !rule.re.MatchString(folded) {
			continue
		}
		if best.re == nil || rule.Confidence > best.Confidence {
			best = rule
		}
	}
	return best
}
