package agent

import (
	"bytes"

	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

func cloneAuthority(value report.Authority) report.Authority {
	value.AnalysisJSON = bytes.Clone(value.AnalysisJSON)
	value.ImmutableFields = cloneStrings(value.ImmutableFields)
	return value
}

func narrativeInputFromAuthority(value report.Authority) report.NarrativeInput {
	return report.NarrativeInput{
		AnalysisJSON:    bytes.Clone(value.AnalysisJSON),
		Evidence:        []report.Evidence{},
		ImmutableFields: cloneStrings(value.ImmutableFields),
	}
}

func cloneNarrativeInput(value report.NarrativeInput) report.NarrativeInput {
	value.AnalysisJSON = bytes.Clone(value.AnalysisJSON)
	value.ImmutableFields = cloneStrings(value.ImmutableFields)
	value.Evidence = cloneEvidenceSlice(value.Evidence)
	return value
}

func cloneNarrative(value report.Narrative) report.Narrative {
	value.KeyFindings = cloneStrings(value.KeyFindings)
	value.Actions = cloneStrings(value.Actions)
	value.Caveats = cloneStrings(value.Caveats)
	value.Source = cloneProvenance(value.Source)
	return value
}

func cloneEvidenceSlice(values []report.Evidence) []report.Evidence {
	if values == nil {
		return nil
	}
	result := make([]report.Evidence, len(values))
	for index, value := range values {
		value.Source = cloneProvenance(value.Source)
		result[index] = value
	}
	return result
}

func cloneProvenance(value provenance.Provenance) provenance.Provenance {
	value.QualityFlags = cloneStrings(value.QualityFlags)
	value.Limitations = cloneStrings(value.Limitations)
	if value.SourceParts != nil {
		parts := make([]provenance.SourcePart, len(value.SourceParts))
		copy(parts, value.SourceParts)
		value.SourceParts = parts
	}
	return value
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	copy(result, values)
	return result
}
