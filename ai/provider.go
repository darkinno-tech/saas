package ai

import "context"

// Role identifies a chat message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a single chat input to a language model.
type Message struct {
	Role    Role
	Content string
}

// Request is a chat completion request.
type Request struct {
	// Model is the target model identifier.
	Model string
	// Messages is the ordered conversation history.
	Messages []Message
	// Options carries provider-specific parameters (temperature, tools, seed, ...).
	Options map[string]any
}

// Response is a chat completion result.
type Response struct {
	Model   string
	Content string
	Usage   Usage
}

// EmbeddingInput is a batch of texts to embed.
type EmbeddingInput struct {
	Model string
	Texts []string
}

// EmbeddingResult is a batch of embedding vectors and their usage.
type EmbeddingResult struct {
	Model      string
	Embeddings [][]float32
	Usage      Usage
}

// Provider is the client abstraction for a language-model backend.
type Provider interface {
	// Name returns the unique provider identifier.
	Name() string
	// Chat runs a chat completion and returns the generated text.
	Chat(ctx context.Context, request Request) (Response, error)
}

// EmbeddingProvider is an optional Provider that also returns embeddings.
type EmbeddingProvider interface {
	Provider
	Embed(ctx context.Context, input EmbeddingInput) (EmbeddingResult, error)
}
