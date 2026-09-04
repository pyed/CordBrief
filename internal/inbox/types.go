package inbox

import (
	"time"

	"cordbrief/internal/digest"
)

// DigestSummary provides compact metadata for rendering digests in inbox lists.
type DigestSummary struct {
	BatchID              string              `json:"batch_id"`
	Title                string              `json:"title"`
	Overview             string              `json:"overview"`
	CreatedAt            time.Time           `json:"created_at"`
	Provider             string              `json:"provider"`
	Model                string              `json:"model"`
	IncludedMessageCount int                 `json:"included_message_count"`
	Trigger              *digest.TriggerInfo `json:"trigger,omitempty"`
}
