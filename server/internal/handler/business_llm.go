package handler

import "github.com/multica-ai/multica/server/pkg/llm"

// businessLLM returns the fixed registry handle when per-business config is
// enabled. A nil result deliberately means the caller should use its legacy
// client and prompt path, preserving migration compatibility and test seams.
func (h *Handler) businessLLM(business llm.Business) *llm.BusinessClient {
	if h == nil || h.LLMRegistry == nil {
		return nil
	}
	return h.LLMRegistry.Client(business)
}
