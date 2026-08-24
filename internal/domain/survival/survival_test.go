package survival

import (
	"testing"
	"time"
)

func TestScenarioValidateRequiresSyntheticInput(t *testing.T) {
	scenario := Scenario{
		ID: "scenario-1", AsOf: time.Now().UTC(), ElapsedMinutes: 60,
		InputCompleteness: 0.8, Synthetic: true,
	}
	if err := scenario.Validate(); err != nil {
		t.Fatal(err)
	}

	scenario.Synthetic = false
	if err := scenario.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝非合成场景")
	}
}
