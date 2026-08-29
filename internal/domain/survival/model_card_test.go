package survival

import (
	"errors"
	"strings"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain"
)

func TestDefaultModelCardDeclaresSafetyBoundaries(t *testing.T) {
	card := DefaultModelCard()
	if card.ModelVersion != ModelVersion || card.Name == "" || card.Purpose == "" ||
		card.Scope == "" || len(card.Inputs) == 0 || len(card.Outputs) == 0 || len(card.Limitations) == 0 {
		t.Fatalf("模型卡字段不完整: %+v", card)
	}
	if card.Review == "" {
		t.Fatal("模型卡缺少人工复核要求")
	}
	if err := card.Validate(); err != nil {
		t.Fatalf("模型卡校验失败: %v", err)
	}
	card.ModelVersion = "outdated-model"
	if !errors.Is(card.Validate(), domain.ErrInvalidInput) {
		t.Fatal("模型卡未拒绝与评估不一致的版本")
	}
}

func TestModelCardRejectsOversizedFieldsAndArrays(t *testing.T) {
	card := DefaultModelCard()
	card.Purpose = strings.Repeat("x", maxLongTextBytes+1)
	if !errors.Is(card.Validate(), domain.ErrInvalidInput) {
		t.Fatal("ModelCard.Validate() 未拒绝超长用途")
	}
	card = DefaultModelCard()
	card.Inputs = make([]string, maxTextItems+1)
	for index := range card.Inputs {
		card.Inputs[index] = "输入"
	}
	if !errors.Is(card.Validate(), domain.ErrInvalidInput) {
		t.Fatal("ModelCard.Validate() 未拒绝超量输入")
	}
}
