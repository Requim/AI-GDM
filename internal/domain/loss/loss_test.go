package loss

import (
	"testing"
	"time"
)

func TestCostBaselineValidate(t *testing.T) {
	valid := CostBaseline{
		Unit: "平方米", LowCents: 100, CentralCents: 200, HighCents: 300,
		Currency: "CNY", PriceBaseDate: time.Now().UTC(), Status: BaselineDemoOnly,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	invalid := valid
	invalid.LowCents = 400
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate() 未拒绝无序情景带")
	}
}
