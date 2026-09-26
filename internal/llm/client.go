package llm

import "context"

// Client interface defines the contract for LLM clients
type Client interface {
	Complete(
		ctx context.Context,
		req CompletionRequest,
	) (CompletionResponse, error)
}

// CompletionRequest represents a request to the LLM
type CompletionRequest struct {
	Messages []Message `json:"messages"`
	// Add other request parameters as needed
}

// Message represents a message in the conversation
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompletionResponse represents a response from the LLM
type CompletionResponse struct {
	Content string `json:"content"`
	// Add other response fields as needed
}
