package openai

import (
	"errors"
	"strings"
	"testing"
)

func TestRequestProcessingClassificationDoesNotExposeCause(t *testing.T) {
	raw := errors.New("EOF reading response from https://private-host?api_key=secret")
	err := requestError(raw)
	if !errors.Is(err, ErrRequestProcessing) || errors.Is(err, raw) || errors.Unwrap(err) != nil {
		t.Fatal("classification missing or private cause retained")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-host") {
		t.Fatal("private diagnostics leaked")
	}
}
