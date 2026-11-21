package claude

import (
	"context"
	"testing"
)

// TestParseReportedCostReadsOnlyAUsableFigure pins R4/R10's parse: a reported
// cost is read, and anything missing or unusable stays unknown rather than
// becoming zero.
func TestParseReportedCostReadsOnlyAUsableFigure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		stdout string
		want   *float64
	}{
		{"result object", `{"type":"result","total_cost_usd":0.42}`, floatPtr(0.42)},
		{"block then result", testResumeOutput + "\n" + `{"type":"result","total_cost_usd":0.42}`, floatPtr(0.42)},
		{"integer figure", `{"total_cost_usd":2}`, floatPtr(2)},
		{"absent", testResumeOutput, nil},
		{"empty", "", nil},
		{"null", `{"total_cost_usd":null}`, nil},
		{"negative", `{"total_cost_usd":-1}`, nil},
		{"not a number", `{"total_cost_usd":"lots"}`, nil},
		{"malformed line", `{"total_cost_usd":`, nil},
	}
	for _, tc := range cases {
		got := ParseReportedCost([]byte(tc.stdout))
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: ParseReportedCost = %v, want unknown (nil)", tc.name, *got)
		case tc.want != nil && got == nil:
			t.Errorf("%s: ParseReportedCost = nil, want %v", tc.name, *tc.want)
		case tc.want != nil && *got != *tc.want:
			t.Errorf("%s: ParseReportedCost = %v, want %v", tc.name, *got, *tc.want)
		}
	}
}

// TestResumeRecordsTheCostTheCLIReported is R4 end to end: the figure the CLI
// printed on the resume's stdout reaches ResumeResult, which is what the
// recovery engine persists.
func TestResumeRecordsTheCostTheCLIReported(t *testing.T) {
	t.Parallel()

	cli, _ := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(testResumeOutput + "\n" + `{"type":"result","total_cost_usd":0.42}`)},
	)
	result, err := cli.Resume(context.Background(), testSessionID, "keep going", testSessionDir)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if result.TotalCostUSD == nil {
		t.Fatal("TotalCostUSD = nil, want the 0.42 the CLI reported")
	}
	if *result.TotalCostUSD != 0.42 {
		t.Errorf("TotalCostUSD = %v, want 0.42", *result.TotalCostUSD)
	}
}

// TestResumeLeavesAnUnreportedCostUnknown is the other half of R10: a resume
// that reports no price does not look free.
func TestResumeLeavesAnUnreportedCostUnknown(t *testing.T) {
	t.Parallel()

	cli, _ := newTestCLI(t,
		Output{Stdout: []byte(testVersionOutput)},
		Output{Stdout: []byte(testResumeOutput)},
	)
	result, err := cli.Resume(context.Background(), testSessionID, "keep going", testSessionDir)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if result.TotalCostUSD != nil {
		t.Errorf("TotalCostUSD = %v, want nil: an unreported cost is unknown, not zero", *result.TotalCostUSD)
	}
}

func floatPtr(v float64) *float64 { return &v }
