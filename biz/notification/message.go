package notification

import (
	"strings"

	"github.com/im10furry/saas/core/types"
)

type Message struct {
	ID       string
	TenantID types.TenantID
	Channel  string
	To       string
	Subject  string
	Body     string
	Metadata map[string]string
	Tags     map[string]string
}

// Validate verifies the fields required by all notification senders.
func (message Message) Validate() error {
	if message.TenantID == "" || strings.TrimSpace(message.Channel) == "" || strings.TrimSpace(message.To) == "" {
		return ErrInvalidMessage
	}
	if hasHeaderInjection(message.ID) || hasHeaderInjection(message.Channel) {
		return ErrInvalidMessage
	}
	return nil
}

// Clone returns a deep copy of message metadata and provider-visible tags.
func (message Message) Clone() Message {
	message.Metadata = cloneStringMap(message.Metadata)
	message.Tags = cloneStringMap(message.Tags)
	return message
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
