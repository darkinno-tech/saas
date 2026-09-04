package ai

import "context"

// MockProvider returns a fixed chat response for testing without a live backend.
type MockProvider struct {
	NameID  string
	Reply   string
	ChatErr error
	Usage   Usage
	Calls   int
}

var _ Provider = (*MockProvider)(nil)

// Name implements Provider.
func (provider *MockProvider) Name() string { return provider.NameID }

// Chat implements Provider and increments Calls.
func (provider *MockProvider) Chat(_ context.Context, request Request) (Response, error) {
	provider.Calls++
	if provider.ChatErr != nil {
		return Response{}, provider.ChatErr
	}
	return Response{
		Model:   request.Model,
		Content: provider.Reply,
		Usage:   provider.Usage,
	}, nil
}

// MockEmbedding is a MockProvider that also returns embeddings.
type MockEmbedding struct {
	NameID     string
	Reply      string
	ChatErr    error
	EmbedErr   error
	Usage      Usage
	Vectors    [][]float32
	ChatCalls  int
	EmbedCalls int
}

var _ EmbeddingProvider = (*MockEmbedding)(nil)

// Name implements Provider.
func (provider *MockEmbedding) Name() string { return provider.NameID }

// Chat implements Provider.
func (provider *MockEmbedding) Chat(_ context.Context, request Request) (Response, error) {
	provider.ChatCalls++
	if provider.ChatErr != nil {
		return Response{}, provider.ChatErr
	}
	return Response{
		Model:   request.Model,
		Content: provider.Reply,
		Usage:   provider.Usage,
	}, nil
}

// Embed implements EmbeddingProvider.
func (provider *MockEmbedding) Embed(_ context.Context, input EmbeddingInput) (EmbeddingResult, error) {
	provider.EmbedCalls++
	if provider.EmbedErr != nil {
		return EmbeddingResult{}, provider.EmbedErr
	}
	return EmbeddingResult{
		Model:      input.Model,
		Embeddings: provider.Vectors,
		Usage:      provider.Usage,
	}, nil
}
