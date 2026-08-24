package hazard

import "testing"

func TestValidateThresholds(t *testing.T) {
	valid := []RiskThreshold{
		{Level: RiskLow, Minimum: 0, Maximum: 0.1},
		{Level: RiskModerate, Minimum: 0.1, Maximum: 0.5},
		{Level: RiskHigh, Minimum: 0.5, Maximum: 1},
	}
	if err := ValidateThresholds(valid); err != nil {
		t.Fatal(err)
	}

	invalid := []RiskThreshold{{Level: RiskLow, Minimum: 0.2, Maximum: 0.1}}
	if err := ValidateThresholds(invalid); err == nil {
		t.Fatal("ValidateThresholds() 未拒绝反向区间")
	}

	incomplete := []RiskThreshold{{Level: RiskHigh, Minimum: 0.5, Maximum: 1}}
	if err := ValidateThresholds(incomplete); err == nil {
		t.Fatal("ValidateThresholds() 未拒绝不完整覆盖")
	}
}
