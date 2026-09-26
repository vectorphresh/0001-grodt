package llm

import (
	"context"
	"fmt"
)

type FakeClient struct {
	Responses []CompletionResponse
	Requests  []CompletionRequest
}

func NewFakeClient(responses []CompletionResponse) *FakeClient {
	return &FakeClient{Responses: responses}
}
func (c *FakeClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if err := ctx.Err(); err != nil {
		return CompletionResponse{}, err
	}
	c.Requests = append(c.Requests, req)
	if len(c.Responses) == 0 {
		return CompletionResponse{}, fmt.Errorf("no more responses available")
	}
	response := c.Responses[0]
	c.Responses = c.Responses[1:]
	return response, nil
}
