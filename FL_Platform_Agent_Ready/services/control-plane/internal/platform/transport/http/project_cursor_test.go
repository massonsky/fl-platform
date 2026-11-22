package transport

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestProjectCursorBindsScopeKeysLifetimeAndVersion(t *testing.T) {
	s := Server{CursorKey: []byte("isolated-project-cursor-key")}
	cut := time.Now().UTC().Add(-time.Second)
	key := cut.Add(-time.Minute)
	c := projectCursor{Version: 2, Scope: projectScope(uuid.NewString(), uuid.NewString(), "", "projects"), Cut: cut, AfterAt: &key, AfterID: uuid.NewString(), Expires: cut.Add(15 * time.Minute).Unix()}
	encoded := s.encodeProjectCursor(c)
	if got, err := s.decodeProjectCursor(encoded, c.Scope, true); err != nil || got.AfterID != c.AfterID {
		t.Fatal(got, err)
	}
	for name, mutate := range map[string]func(*projectCursor){
		"version":           func(c *projectCursor) { c.Version = 1 },
		"scope":             func(c *projectCursor) { c.Scope += "/foreign" },
		"expiry":            func(c *projectCursor) { c.Expires = time.Now().Unix() - 1 },
		"extended lifetime": func(c *projectCursor) { c.Expires = c.Cut.Add(time.Hour).Unix() },
		"future cut":        func(c *projectCursor) { c.Cut = time.Now().Add(time.Hour) },
		"zero cut":          func(c *projectCursor) { c.Cut = time.Time{} },
		"invalid UUID":      func(c *projectCursor) { c.AfterID = "foreign" },
		"empty UUID":        func(c *projectCursor) { c.AfterID = uuid.Nil.String() },
		"wrong key shape":   func(c *projectCursor) { c.AfterAt = nil },
		"after beyond cut":  func(c *projectCursor) { v := c.Cut.Add(time.Second); c.AfterAt = &v },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := c
			mutate(&invalid)
			if _, err := s.decodeProjectCursor(s.encodeProjectCursor(invalid), c.Scope, true); err == nil {
				t.Fatal("invalid cursor accepted")
			}
		})
	}
	for _, raw := range []string{"", "invalid!", strings.Repeat("a", 2049), encoded[:len(encoded)-2] + "xx"} {
		if _, err := s.decodeProjectCursor(raw, c.Scope, true); err == nil {
			t.Fatal("tampered cursor accepted")
		}
	}
	raw, _ := json.Marshal(c)
	for _, changed := range [][]byte{append(append([]byte{}, raw...), []byte(" {}")...), append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)} {
		forged := base64.RawURLEncoding.EncodeToString(append(changed, s.sign(changed)...))
		if _, err := s.decodeProjectCursor(forged, c.Scope, true); err == nil {
			t.Fatal("trailing/unknown signed field accepted")
		}
	}
	if _, err := s.decodeProjectCursor(encoded, c.Scope, false); err == nil {
		t.Fatal("project cursor reused as roster cursor")
	}
	c.AfterAt = nil
	if _, err := s.decodeProjectCursor(s.encodeProjectCursor(c), c.Scope, false); err != nil {
		t.Fatal(err)
	}
}

func TestProjectPageQueryRejectsAmbiguousInput(t *testing.T) {
	s := Server{CursorKey: []byte("isolated-project-cursor-key")}
	limit, _, err := s.projectPageQuery(url.Values{}, "scope", true)
	if err != nil || limit != 50 {
		t.Fatal(limit, err)
	}
	for _, query := range []url.Values{{"limit": {"0"}}, {"limit": {"101"}}, {"limit": {"1", "2"}}, {"limit": {""}}, {"cursor": {""}}, {"cursor": {"a", "b"}}, {"org_id": {"other"}}, {"filter": {"hidden"}}} {
		if _, _, err := s.projectPageQuery(query, "scope", true); err == nil {
			t.Fatal("invalid query accepted", query)
		}
	}
	cut := time.Now().Add(-time.Second)
	key := cut.Add(-time.Minute)
	c := projectCursor{Version: 2, Scope: "scope", Cut: cut, AfterAt: &key, AfterID: uuid.NewString(), Expires: cut.Add(15 * time.Minute).Unix()}
	limit, _, err = s.projectPageQuery(url.Values{"limit": {"100"}, "cursor": {s.encodeProjectCursor(c)}}, "scope", true)
	if err != nil || limit != 100 {
		t.Fatal("limit change rejected", limit, err)
	}
}
