package loop

import (
	"encoding/json"
	"errors"

	"github.com/vectorphresh/0001-grodt/internal/openai"
	"github.com/vectorphresh/0001-grodt/internal/toolcall"
)

// Classify provider metadata, never provider prose or instructions.
func modelContextExceeded(err error) bool {
	var failure *openai.ProviderRequestError
	if !errors.As(err, &failure) || failure.StatusCode != 400 {
		return false
	}
	var body struct {
		Type  string `json:"type"`
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	// Some providers use numeric code=400, so decode type independently.
	var typed struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(failure.Response, &typed)
	_ = json.Unmarshal(failure.Response, &body)
	for _, code := range []string{typed.Type, body.Type, body.Code, body.Error.Code, body.Error.Type} {
		if code == "exceed_context_size_error" || code == "context_length_exceeded" {
			return true
		}
	}
	return false
}

func (p *ToolProvider) actorModelState(budget int) json.RawMessage {
	raw := p.Store.ModelJSON()
	if budget == 0 {
		return raw
	}
	var view map[string]json.RawMessage
	_ = json.Unmarshal(raw, &view)
	delete(view, "knowledge")
	// Preserve task/plan state; expose exact, bounded evidence with explicit
	// coverage instead of treating omitted payloads as complete observations.
	view["knowledge_projection"] = mustEncode(p.Store.ProjectKnowledge(p.Store.Snapshot(), nil, budget, 64, min(16<<10, budget/2)))
	return mustEncode(view)
}

func compactActorMessages(messages []toolcall.Message, budget int) []toolcall.Message {
	if budget == 0 {
		return messages
	}
	if budget < 8<<10 && len(messages) > 0 {
		return []toolcall.Message{messages[0], {Role: "user", Text: "Recent tool exchange omitted to repair context size. Use accepted state and evidence coverage. Previously executed tools must not be repeated merely because their payloads are omitted."}}
	}
	for i := range messages {
		if messages[i].Role == "tool" && len(messages[i].Text) > 4096 {
			messages[i].Text = string(mustEncode(map[string]any{"call_id": messages[i].CallID, "payload_omitted": true, "reason": "context_size_repair", "guidance": "Consult accepted state and evidence coverage; omission does not mean failure, emptiness or that execution should be repeated."}))
		}
	}
	return messages
}
