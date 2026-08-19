package ai

// Usage reports the tokens consumed by one AI call (or the sum of several):
// Prompt is the input tokens, Completion the generated ones.
type Usage struct {
	Prompt     int
	Completion int
}

// Total returns the total billed tokens (prompt + completion).
func (u Usage) Total() int { return u.Prompt + u.Completion }

// Add returns the sum of u and o.
func (u Usage) Add(o Usage) Usage {
	return Usage{Prompt: u.Prompt + o.Prompt, Completion: u.Completion + o.Completion}
}

// wireUsage is the usage block OpenAI-compatible APIs return.
type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func (u wireUsage) usage() Usage {
	return Usage{Prompt: u.PromptTokens, Completion: u.CompletionTokens}
}

// streamOptions asks the provider to report token usage in the final
// streaming chunk.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// estimateTokens approximates a token count as ceil(chars/4), at least 1 when
// there is content.
func estimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

// fallbackUsage estimates one call's usage when the provider reports none:
// promptChars are the characters of every message content sent, completion
// chars the length of the returned text.
func fallbackUsage(promptChars int, completion string) Usage {
	return Usage{Prompt: estimateTokens(promptChars), Completion: estimateTokens(len(completion))}
}

// messageChars counts the content characters of msgs, including tool-call
// names and arguments (billed as prompt tokens too).
func messageChars(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return n
}
