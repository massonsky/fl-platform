package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const textFrame = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Привет\"},\"finish_reason\":null}]}\n\n"
const endFrame = "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

func TestChatStreamProtocol(t *testing.T) {
	for _, tc := range []struct{ name, body, code string }{
		{"complete", ": keep-alive\n\n" + textFrame + endFrame, ""},
		{"disconnect", textFrame, "PROVIDER_STREAM_INTERRUPTED"},
		{"missing_finish", textFrame + "data: [DONE]\n\n", "UNSUPPORTED_MODEL_OUTPUT"},
		{"empty", endFrame, "UNSUPPORTED_MODEL_OUTPUT"},
		{"upstream_error", "data: {\"error\":{\"message\":\"do-not-leak\"}}\n\n", "PROVIDER_STREAM_ERROR"},
		{"tools", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{}]}}]}\n\n", "UNSUPPORTED_MODEL_OUTPUT"},
		{"malformed", "data: nope\n\n", "INVALID_PROVIDER_RESPONSE"},
		{"limit", strings.Repeat(": heartbeat\n", 100000), "RESPONSE_TOO_LARGE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := ""
			reason, usage, err := readChatStream(strings.NewReader(tc.body), func(s string) error { text += s; return nil })
			if tc.code == "" {
				if err != nil || text != "Привет" || reason != "stop" || usage != nil {
					t.Fatal(reason, usage, err)
				}
				return
			}
			var f Failure
			if !errors.As(err, &f) || f.Code != tc.code {
				t.Fatalf("wanted %s got %v", tc.code, err)
			}
		})
	}
}
func TestChatTransportAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("invalid request")
		}
		var input struct {
			Model    string        `json:"model"`
			Stream   bool          `json:"stream"`
			Limit    int           `json:"max_tokens"`
			Messages []ChatMessage `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || !input.Stream || input.Limit != 1024 || input.Model != "fixture" || len(input.Messages) != 1 {
			t.Error("invalid payload")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, textFrame+endFrame)
	}))
	defer server.Close()
	_, _, err := fetchChat(context.Background(), server.Client(), server.URL+"/v1", "fixture", "fixture", []ChatMessage{{Role: "user", Content: "hello"}}, func(string) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal("emitter cancellation lost", err)
	}
}
