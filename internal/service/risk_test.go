package service

import (
	"math"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
)

func TestEvalRiskFormulaSupportsArithmeticAndGroupConditions(t *testing.T) {
	variables := map[string]float64{
		"total_spend":    25,
		"total_recharge": 100,
		"group_id":       2,
		"risk_score":     12,
		"total_tokens":   5000,
	}
	got, err := evalRiskFormula("choose(group_id == 2, 50 + total_spend * 0.2, 100)", variables)
	if err != nil {
		t.Fatalf("evalRiskFormula returned an error: %v", err)
	}
	if math.Abs(got-55) > 1e-9 {
		t.Fatalf("formula result = %v, want 55", got)
	}
	got, err = evalRiskFormula("min(total_tokens / 1000, 4) + abs(-2)", variables)
	if err != nil {
		t.Fatalf("evalRiskFormula returned an error: %v", err)
	}
	if math.Abs(got-6) > 1e-9 {
		t.Fatalf("formula result = %v, want 6", got)
	}
	got, err = evalRiskFormula("choose(group_id == 2, 10, 1 / 0)", variables)
	if err != nil || got != 10 {
		t.Fatalf("choose() did not lazily evaluate its selected branch: result=%v err=%v", got, err)
	}
}

func TestEvalRiskFormulaRejectsUnsafeOrInvalidExpressions(t *testing.T) {
	variables := map[string]float64{"risk_score": 10}
	for _, expression := range []string{
		"os.Exit(1)",
		"risk_score / 0",
		"unknown_variable + 1",
		"risk_score > 2",
	} {
		if _, err := evalRiskFormula(expression, variables); err == nil {
			t.Errorf("evalRiskFormula(%q) unexpectedly succeeded", expression)
		}
	}
}

func TestRiskRuleMatchesKeywordAndRegex(t *testing.T) {
	if !riskRuleMatches(model.RiskRule{MatchType: "contains", Pattern: "BAN THIS"}, "please ban this request") {
		t.Fatal("case-insensitive contains rule did not match")
	}
	if !riskRuleMatches(model.RiskRule{MatchType: "regex", Pattern: `(?i)secret-[0-9]+`}, "found SECRET-123") {
		t.Fatal("regular expression rule did not match")
	}
	if riskRuleMatches(model.RiskRule{MatchType: "regex", Pattern: "["}, "anything") {
		t.Fatal("invalid regular expression unexpectedly matched")
	}
}

func TestExtractRiskTextFromOpenAIAndResponsesStreams(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\" world\"}\n\n" +
		"data: [DONE]\n")
	if got := extractRiskTextFromStream(body); got != "hello world" {
		t.Fatalf("extractRiskTextFromStream() = %q, want %q", got, "hello world")
	}
}
