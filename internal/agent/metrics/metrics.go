package metrics

// Metrics is the provider-neutral superset of typed agent session metrics. Each
// runtime populates only the subset it tracks; the remaining fields stay at
// their zero values. Presence is explicit, including measured zeros; identity,
// warning text, and session success are never evidence of measurement.
type Metrics struct {
	Availability            Availability
	InputTokensPresent      bool
	OutputTokensPresent     bool
	ReasoningTokensPresent  bool
	CacheReadTokensPresent  bool
	CacheWriteTokensPresent bool
	TotalTokensPresent      bool
	CostPresent             bool
	SessionID               string
	ProviderID              string
	ModelID                 string
	InputTokens             int64
	OutputTokens            int64
	ReasoningTokens         int64
	CacheReadTokens         int64
	CacheWriteTokens        int64
	TotalTokens             int64
	Cost                    float64
	TotalMessages           int64
	UserMessages            int64
	AssistantMessages       int64
	ErroredMessages         int64
	ToolCalls               int64
}
