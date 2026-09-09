package main

import (
	"context"
	"testing"

	"github.com/Requim/AI-GDM/internal/application/loss"
)

func TestFixtureRegionalEstimateHasCompleteBaselineBinding(t *testing.T) {
	scenarios, err := newScenarioStore()
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&fixtureLossEstimator{scenarios: scenarios}).Estimate(context.Background(),
		loss.EstimateInput{SnapshotID: lossSnapshotID, RegionCode: "CN-130100"})
	if err != nil {
		t.Fatal(err)
	}
}
