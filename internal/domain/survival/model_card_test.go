package survival

import "testing"

func TestDefaultModelCardDeclaresSafetyBoundaries(t *testing.T) {
	card := DefaultModelCard()
	if card.ModelVersion != ModelVersion || card.Name == "" || card.Purpose == "" ||
		card.Scope == "" || len(card.Inputs) == 0 || len(card.Outputs) == 0 || len(card.Limitations) == 0 {
		t.Fatalf("模型卡字段不完整: %+v", card)
	}
	if card.Review == "" {
		t.Fatal("模型卡缺少人工复核要求")
	}
}
