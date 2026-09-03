package digest

// Digest represents the structured digest produced by the LLM.
type Digest struct {
	Overview     string             `json:"overview"`
	Items        []Item             `json:"items"`
	WorthOpening []WorthOpeningItem `json:"worth_opening"`
}

// Item represents a single summarized point with local source attributions.
type Item struct {
	Category  string   `json:"category"`
	Text      string   `json:"text"`
	SourceIDs []string `json:"source_ids"`
}

// WorthOpeningItem represents a specific message or exchange worth reading in Discord.
type WorthOpeningItem struct {
	Text     string `json:"text"`
	SourceID string `json:"source_id"`
}
