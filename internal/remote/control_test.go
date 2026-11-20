package remote

import (
	"context"
	"encoding/json"
	"testing"
)

func TestApprovalAndAnswerReturnReceiptWithoutCallingCodex(t *testing.T) {
	handler := Handler{}
	for _, response := range []json.RawMessage{
		json.RawMessage(`{"kind":"approval","decision":"allow_once"}`),
		json.RawMessage(`{"kind":"answer","answers":{"q1":["B"]}}`),
	} {
		id := NewID()
		raw, err := json.Marshal(map[string]any{
			"v": 1, "type": "request", "id": id, "method": "interaction.respond",
			"params": map[string]any{
				"session_id": "session", "interaction_id": NewID(), "idempotency_key": NewID(),
				"response": response,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		var result message
		if err := json.Unmarshal(handler.Handle(context.Background(), "phone", raw), &result); err != nil ||
			result.ID != id || result.Error == nil || result.Error.Code != "unavailable" ||
			result.Error.Reason != "control_not_enabled" || len(result.Result) != 0 {
			t.Fatalf("invalid receipt: %+v %v", result, err)
		}
	}
}
