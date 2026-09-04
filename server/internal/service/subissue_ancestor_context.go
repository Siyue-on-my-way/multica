package service

import "strings"

// includeAncestorBriefInSubissueDescriptions makes the background snapshot a
// durable part of the generated child description. The model receives the
// brief as prompt context, but it is not reliable about repeating every
// ancestor (in particular the oldest root issue) in its output. Keeping the
// bounded, source-labelled brief here ensures the created issue retains the
// context that was actually used for generation. An empty brief is the
// explicit opt-out path and leaves descriptions unchanged.
func includeAncestorBriefInSubissueDescriptions(details []SubissueSuggestion, ancestorBrief string) []SubissueSuggestion {
	for index := range details {
		details[index].Description = appendAncestorBriefToDescription(details[index].Description, ancestorBrief)
	}
	return details
}

func appendAncestorBriefToDescription(description, ancestorBrief string) string {
	description = strings.TrimSpace(description)
	ancestorBrief = strings.TrimSpace(ancestorBrief)
	if ancestorBrief == "" {
		return description
	}
	// A compliant model may already copy the complete brief. Avoid duplicating
	// it while still appending it when the model only paraphrases or omits it.
	if strings.Contains(description, ancestorBrief) {
		return description
	}
	if description == "" {
		return ancestorBrief
	}
	return description + "\n\n" + ancestorBrief
}
