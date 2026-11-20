package recovery

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// SpendLimits is PRD 5's pair of ccresu-enforced caps.
//
// They exist because the flag that used to bound a run's cost,
// `--max-budget-usd`, is documented as print mode only and therefore cannot ride
// a `--bg` handoff. What ccresu can bound is the series of attempts: it sums the
// costs the CLI reported and refuses to start another attempt at the cap (R5),
// and it stops when a single attempt overspends (R6).
//
// Zero means off, and off means today's behaviour exactly: no sum is taken and no
// check runs. A non-zero default would stop recovery on an upgrade nobody asked
// to change (R12).
type SpendLimits struct {
	// PerSession refuses a new attempt once the session's recorded spend reaches
	// it (R5).
	PerSession float64
	// PerAttempt stops automatic recovery when one attempt reports more than it
	// (R6).
	PerAttempt float64
}

// SessionCapped reports whether the session-level cap is configured.
func (l SpendLimits) SessionCapped() bool { return l.PerSession > 0 }

// AttemptCapped reports whether the per-attempt cap is configured.
func (l SpendLimits) AttemptCapped() bool { return l.PerAttempt > 0 }

// Configured reports whether either cap is on.
func (l SpendLimits) Configured() bool { return l.SessionCapped() || l.AttemptCapped() }

// formatUSD renders a dollar figure the way an operator reads one: with a dollar
// sign, no trailing zeroes it did not need, and never a rounded-away cap. A cap
// of $0.005 printed as "$0.01" would misstate what was configured, so the value
// is formatted exactly rather than to a fixed number of places.
func formatUSD(v float64) string {
	return "$" + strconv.FormatFloat(v, 'f', -1, 64)
}

// sessionCapError is R7's named outcome for the session cap: the text names the
// knob, the configured cap and the observed spend, because that text is what
// reaches the operator through EventData.Reason.
func sessionCapError(limit float64, spend storage.SpendRecord) error {
	return fmt.Errorf("%w: recovery.guardrails.max_cost_usd_per_session is %s and the session has recorded %s across %d attempt(s)",
		ErrSpendCapReached, formatUSD(limit), formatUSD(spend.KnownUSD), spend.Known+spend.Unknown)
}

// attemptCapError is R7's named outcome for the per-attempt cap.
func attemptCapError(limit float64, attempt storage.RecoveryAttempt, cost float64) error {
	return fmt.Errorf("%w: recovery.guardrails.max_cost_usd_per_attempt is %s and attempt %d recorded %s",
		ErrSpendCapReached, formatUSD(limit), attempt.AttemptNumber, formatUSD(cost))
}

// Spend reports what a session's attempts are known to have cost.
//
// It is exported so `ccresu inspect` and `ccresu history` can show the same
// figure the cap is compared against, instead of a second opinion computed
// somewhere else.
func (m *Manager) Spend(ctx context.Context, sessionID string) (storage.SpendRecord, error) {
	return m.store.SessionSpend(ctx, sessionID)
}

// Limits reports the configured caps, so a caller can show them next to the
// spend it just read.
func (m *Manager) Limits() SpendLimits { return m.limits }

// checkSessionCap is R5's pre-attempt check.
//
// It runs before StartAttempt, so a refusal spends nothing \u2014 the same rule the
// attempt budget follows, where a request that spends no attempt has started
// nothing. It sums the database rather than trusting a figure read back from the
// CLI, because a resumed conversation's own running total does not include the
// work done before the resume.
//
// manual is R11: an explicit `ccresu recover` is a human act, and a cap that
// exists to replace an absent human must not bind one who is present. The
// override is logged rather than silent, because the operator should know they
// just walked past a limit they set.
func (m *Manager) checkSessionCap(ctx context.Context, sessionID string, manual bool) error {
	if !m.limits.SessionCapped() {
		return nil
	}
	spend, err := m.store.SessionSpend(ctx, sessionID)
	if err != nil {
		// A cap that cannot be evaluated must not stop a resume: that would turn a
		// database problem into a spending freeze. It is logged and the attempt
		// proceeds, exactly as R10 requires for an unmeasurable cost.
		m.log.Warn("read session spend; continuing without the session cap",
			"session", sessionID, "error", err)
		return nil
	}
	if spend.KnownUSD < m.limits.PerSession {
		return nil
	}
	cause := sessionCapError(m.limits.PerSession, spend)
	if manual {
		m.log.Warn("a manual recovery overrode the session spend cap",
			"session", sessionID, "cap", m.limits.PerSession, "spent", spend.KnownUSD)
		return nil
	}
	return cause
}

// checkAttemptCap is R6's post-attempt check.
//
// It is a post-hoc guard for the case the session cap cannot see: one very
// expensive turn. A nil cost is unknown, and an unknown cost never trips a cap
// (R10) \u2014 the attempt is recorded as unmeasured and stays ungoverned, which the
// PRD states plainly rather than papering over.
func (m *Manager) checkAttemptCap(attempt storage.RecoveryAttempt, cost *float64) error {
	if !m.limits.AttemptCapped() || cost == nil {
		return nil
	}
	if *cost <= m.limits.PerAttempt {
		return nil
	}
	return attemptCapError(m.limits.PerAttempt, attempt, *cost)
}

// There is deliberately no UnparkAffordable here any more.
//
// It existed to release sessions parked in SPEND_CAP_REACHED once the operator
// raised the cap. That state is gone: with seven states a spend stop is DISABLED
// (see Class.State), because "nothing may be spent until a human decides" is what
// DISABLED means, and the way back is `ccresu enable` — one command, no daemon
// stage, and no way for a pass to resume a session whose cap it just refused.
// knownCost is the cost an attempt may be judged by: what the CLI reported, with
// an absent or unusable figure reading as unknown rather than as zero.
func knownCost(reported *float64) *float64 { return claude.KnownCost(reported) }
