package survival

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Requim/AI-GDM/internal/domain"
)

const (
	maxIdentifierBytes = 128
	maxShortTextBytes  = 256
	maxLongTextBytes   = 1024
	maxTextItems       = 32
)

func validateRequiredText(name, value string, maximum int) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > maximum {
		return fmt.Errorf("%w: %s长度或编码无效", domain.ErrInvalidInput, name)
	}
	return nil
}

func validateOptionalText(name, value string, maximum int) error {
	if value == "" {
		return nil
	}
	return validateRequiredText(name, value, maximum)
}

func validateTextList(name string, values []string, maximumItems, maximumBytes int) error {
	if len(values) > maximumItems {
		return fmt.Errorf("%w: %s超过 %d 项", domain.ErrInvalidInput, name, maximumItems)
	}
	for index, value := range values {
		if err := validateRequiredText(fmt.Sprintf("%s第 %d 项", name, index+1), value, maximumBytes); err != nil {
			return err
		}
	}
	return nil
}
