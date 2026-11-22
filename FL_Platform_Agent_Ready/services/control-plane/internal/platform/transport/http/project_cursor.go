package transport

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
)

type projectCursor struct {
	Version int        `json:"v"`
	Scope   string     `json:"scope"`
	Cut     time.Time  `json:"cut"`
	AfterAt *time.Time `json:"after_at,omitempty"`
	AfterID string     `json:"after_id"`
	Expires int64      `json:"expires"`
}

func projectScope(actor, org, project, route string) string {
	return strings.Join([]string{"organization-project-v2", actor, org, project, route}, "/")
}
func invalidProjectCursor() error { return experiment.Failure{Status: 400, Code: "INVALID_CURSOR"} }
func (s *Server) encodeProjectCursor(c projectCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(append(raw, s.sign(raw)...))
}
func (s *Server) decodeProjectCursor(encoded, scope string, projects bool) (projectCursor, error) {
	var c projectCursor
	if len(encoded) == 0 || len(encoded) > 2048 {
		return c, invalidProjectCursor()
	}
	packed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(packed) <= sha256.Size {
		return c, invalidProjectCursor()
	}
	raw, signature := packed[:len(packed)-sha256.Size], packed[len(packed)-sha256.Size:]
	if !hmac.Equal(signature, s.sign(raw)) {
		return c, invalidProjectCursor()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || c.Version != 2 || c.Scope != scope || c.Expires <= time.Now().Unix() ||
		c.Cut.Before(time.Unix(0, 0)) || c.Cut.After(time.Now()) || c.Expires > c.Cut.Add(15*time.Minute).Unix() {
		return c, invalidProjectCursor()
	}
	if decoder.Decode(new(any)) != io.EOF {
		return c, invalidProjectCursor()
	}
	id, err := uuid.Parse(c.AfterID)
	if err != nil || id == uuid.Nil || c.AfterID != id.String() || projects != (c.AfterAt != nil) {
		return c, invalidProjectCursor()
	}
	if c.AfterAt != nil && (c.AfterAt.IsZero() || c.AfterAt.After(c.Cut)) {
		return c, invalidProjectCursor()
	}
	return c, nil
}
func (s *Server) projectPageQuery(query url.Values, scope string, projects bool) (int, projectCursor, error) {
	limit, c := 50, projectCursor{Version: 2, Scope: scope}
	for key, values := range query {
		if (key != "limit" && key != "cursor") || len(values) != 1 {
			return 0, c, experiment.Failure{Status: 400, Code: "INVALID_REQUEST"}
		}
	}
	if value, exists := query["limit"]; exists {
		var err error
		limit, err = strconv.Atoi(value[0])
		if err != nil || limit < 1 || limit > 100 {
			return 0, c, experiment.Failure{Status: 400, Code: "INVALID_REQUEST"}
		}
	}
	if value, exists := query["cursor"]; exists {
		var err error
		c, err = s.decodeProjectCursor(value[0], scope, projects)
		if err != nil {
			return 0, c, err
		}
	}
	return limit, c, nil
}
