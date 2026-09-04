package ai

const (
	// DimTurns counts model calls for a tenant.
	DimTurns = "turns"
	// DimInputTokens counts prompt tokens consumed for a tenant.
	DimInputTokens = "tokens_input"
	// DimOutputTokens counts generated tokens consumed for a tenant.
	DimOutputTokens = "tokens_output"
	// DimTotalTokens is the aggregated input plus output token count.
	DimTotalTokens = "tokens_total"
)

// Usage reports the token consumption of a model call.
type Usage struct {
	// Model is the model the usage belongs to (may be empty for aggregates).
	Model string
	// Turns counts the number of model calls, when the caller tracks it.
	Turns int64
	// InputTokens counts prompt tokens consumed.
	InputTokens int64
	// OutputTokens counts generated tokens consumed.
	OutputTokens int64
}

// TotalTokens returns input plus output tokens.
func (usage Usage) TotalTokens() int64 {
	return usage.InputTokens + usage.OutputTokens
}
