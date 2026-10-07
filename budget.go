package claudestream

import (
	"errors"
	"fmt"
	"slices"
)

// Budget lists thresholds on the session's running totals. Each threshold
// yields one BudgetThreshold event the first time its total reaches it,
// after the Result of the turn that reached it. Budgets observe; they never
// stop the session.
type Budget struct {
	// CostThresholds are in US dollars, against Result.TotalCostUSD.
	CostThresholds []float64
	// TurnThresholds are against the number of completed turns.
	TurnThresholds []int64
	// TokenThresholds are against the last Result's input plus output
	// tokens.
	TokenThresholds []int64
}

// Validate refuses negative (and NaN) thresholds.
func (b Budget) Validate() error {
	var errs []error
	for _, v := range b.CostThresholds {
		if !(v >= 0) {
			errs = append(errs, fmt.Errorf("CostThresholds contains %v; thresholds must be zero or more", v))
		}
	}
	for _, v := range b.TurnThresholds {
		if v < 0 {
			errs = append(errs, fmt.Errorf("TurnThresholds contains %d; thresholds must be zero or more", v))
		}
	}
	for _, v := range b.TokenThresholds {
		if v < 0 {
			errs = append(errs, fmt.Errorf("TokenThresholds contains %d; thresholds must be zero or more", v))
		}
	}
	return errors.Join(errs...)
}

// sessionTotals are the running totals a session keeps from its Results:
// the cost and tokens as the latest Result reports them (Claude Code reports
// them cumulatively), and the number of turns completed.
type sessionTotals struct {
	CostUSD float64
	Tokens  int64
	Turns   int
}

// add counts one completed turn ending in r.
func (t *sessionTotals) add(r Result) {
	t.Turns++
	if r.Usage != nil {
		t.Tokens = r.Usage.InputTokens + r.Usage.OutputTokens
	}
	t.CostUSD = r.TotalCostUSD
}

// budgetKey identifies a threshold for the fired-once rule.
type budgetKey struct {
	metric    BudgetMetric
	threshold float64
}

// budgetTracker remembers which thresholds of a Budget have fired.
type budgetTracker struct {
	budget *Budget
	fired  map[budgetKey]bool
}

// newBudgetTracker tracks budget; a nil budget never fires.
func newBudgetTracker(budget *Budget) *budgetTracker {
	return &budgetTracker{budget: budget, fired: map[budgetKey]bool{}}
}

// check returns an event for every threshold the totals reach for the first
// time: cost, then turns, then tokens, each in ascending threshold order.
func (t *budgetTracker) check(h EventHeader, totals sessionTotals) []BudgetThreshold {
	if t.budget == nil {
		return nil
	}
	toFloats := func(values []int64) []float64 {
		out := make([]float64, len(values))
		for i, v := range values {
			out[i] = float64(v)
		}
		return out
	}
	checks := []struct {
		metric     BudgetMetric
		thresholds []float64
		current    float64
	}{
		{BudgetMetricCost, t.budget.CostThresholds, totals.CostUSD},
		{BudgetMetricTurns, toFloats(t.budget.TurnThresholds), float64(totals.Turns)},
		{BudgetMetricTokens, toFloats(t.budget.TokenThresholds), float64(totals.Tokens)},
	}
	var events []BudgetThreshold
	for _, c := range checks {
		sorted := slices.Clone(c.thresholds)
		slices.Sort(sorted)
		for _, threshold := range sorted {
			key := budgetKey{metric: c.metric, threshold: threshold}
			if t.fired[key] || c.current < threshold {
				continue
			}
			t.fired[key] = true
			events = append(events, BudgetThreshold{EventHeader: h, Metric: c.metric, Threshold: threshold, CurrentValue: c.current})
		}
	}
	return events
}
