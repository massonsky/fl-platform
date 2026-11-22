package inference

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

func readChatStream(reader io.Reader, emit func(string) error) (string, *Usage, error) {
	limited := &io.LimitedReader{R: reader, N: (1 << 20) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	var frame []string
	var finish string
	var usage *Usage
	size := 0
	process := func() (bool, error) {
		data := strings.Join(frame, "\n")
		frame = nil
		if data == "" {
			return false, nil
		}
		if data == "[DONE]" {
			if finish == "" || size == 0 {
				return false, Failure{502, "UNSUPPORTED_MODEL_OUTPUT"}
			}
			return true, nil
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content *string         `json:"content"`
					Tools   json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				Finish *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return false, Failure{502, "INVALID_PROVIDER_RESPONSE"}
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, Failure{502, "PROVIDER_STREAM_ERROR"}
		}
		if chunk.Usage != nil {
			for _, n := range []*int64{chunk.Usage.Prompt, chunk.Usage.Completion, chunk.Usage.Total} {
				if n != nil && (*n < 0 || *n > 1e12) {
					return false, Failure{502, "INVALID_PROVIDER_RESPONSE"}
				}
			}
			if chunk.Usage.Prompt != nil || chunk.Usage.Completion != nil || chunk.Usage.Total != nil {
				usage = chunk.Usage
			}
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if len(choice.Delta.Tools) > 0 && string(choice.Delta.Tools) != "null" && string(choice.Delta.Tools) != "[]" {
				return false, Failure{502, "UNSUPPORTED_MODEL_OUTPUT"}
			}
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				if finish != "" {
					return false, Failure{502, "INVALID_PROVIDER_RESPONSE"}
				}
				text := *choice.Delta.Content
				if strings.ContainsRune(text, 0) {
					return false, Failure{502, "INVALID_PROVIDER_RESPONSE"}
				}
				size += len(text)
				if size > 1<<20 {
					return false, Failure{502, "RESPONSE_TOO_LARGE"}
				}
				if err := emit(text); err != nil {
					return false, err
				}
			}
			if choice.Finish != nil && *choice.Finish != "" {
				if *choice.Finish != "stop" && *choice.Finish != "length" {
					return false, Failure{502, "UNSUPPORTED_MODEL_OUTPUT"}
				}
				finish = *choice.Finish
			}
		}
		return false, nil
	}
	for scanner.Scan() {
		if limited.N <= 0 {
			return finish, usage, Failure{502, "RESPONSE_TOO_LARGE"}
		}
		line := scanner.Text()
		if line == "" {
			done, err := process()
			if err != nil || done {
				return finish, usage, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			frame = append(frame, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if len(frame) > 0 {
		done, err := process()
		if err != nil || done {
			return finish, usage, err
		}
	}
	if limited.N <= 0 {
		return finish, usage, Failure{502, "RESPONSE_TOO_LARGE"}
	}
	return finish, usage, Failure{502, "PROVIDER_STREAM_INTERRUPTED"}
}
