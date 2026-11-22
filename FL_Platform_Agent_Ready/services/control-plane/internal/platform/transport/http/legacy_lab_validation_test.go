package transport

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func TestLegacyLabInputValidation(t *testing.T) {
	cases := []struct {
		name, body, contentType string
		status                  int
	}{
		{"missing revision", `{"status":"active","reason":"reviewed"}`, "application/json", 400},
		{"null revision", `{"status":"active","revision":null,"reason":"reviewed"}`, "application/json", 400},
		{"negative", `{"status":"active","revision":-1,"reason":"reviewed"}`, "application/json", 400},
		{"fraction", `{"status":"active","revision":0.5,"reason":"reviewed"}`, "application/json", 400},
		{"above SQL int", `{"status":"active","revision":2147483648,"reason":"reviewed"}`, "application/json", 400},
		{"overflow", `{"status":"active","revision":1e100,"reason":"reviewed"}`, "application/json", 400},
		{"string revision", `{"status":"active","revision":"0","reason":"reviewed"}`, "application/json", 400},
		{"status", `{"status":"not_admitted","revision":0,"reason":"reviewed"}`, "application/json", 400},
		{"null reason", `{"status":"active","revision":0,"reason":null}`, "application/json", 400},
		{"blank reason", `{"status":"active","revision":0,"reason":"  "}`, "application/json", 400},
		{"control", `{"status":"active","revision":0,"reason":"line\nbreak"}`, "application/json", 400},
		{"nul", `{"status":"active","revision":0,"reason":"bad\u0000reason"}`, "application/json", 400},
		{"long unicode", `{"status":"active","revision":0,"reason":"` + strings.Repeat("я", 241) + `"}`, "application/json", 400},
		{"unknown", `{"status":"active","revision":0,"reason":"reviewed","source":"bootstrap"}`, "application/json", 400},
		{"duplicate", `{"status":"active","revision":0,"revision":1,"reason":"reviewed"}`, "application/json", 400},
		{"trailing", `{"status":"active","revision":0,"reason":"reviewed"} {}`, "application/json", 400},
		{"invalid UTF8", "{\"status\":\"active\",\"revision\":0,\"reason\":\"\xff\"}", "application/json", 400},
		{"content type", `{}`, "text/plain", 415},
		{"too large", strings.Repeat(" ", 65537), "application/json", 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("PUT", "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			_, err := legacyLabChange(w, r)
			if err == nil {
				t.Fatal("invalid public input accepted")
			}
			failure(w, err)
			if w.Code != tc.status {
				t.Fatalf("got%d want%d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	for _, revision := range []int{0, 2147483647} {
		raw, _ := json.Marshal(map[string]any{"status": "revoked", "revision": revision, "reason": strings.Repeat("я", 240)})
		r := httptest.NewRequest("PUT", "/", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		got, err := legacyLabChange(httptest.NewRecorder(), r)
		if err != nil || got.Revision != revision || len([]rune(got.Reason)) != 240 {
			t.Fatal("valid boundary rejected", err)
		}
	}
	r := httptest.NewRequest("PUT", "/", strings.NewReader(`{"status":"active","revision":0,"reason":"  Проверено  "}`))
	r.Header.Set("Content-Type", "application/json")
	got, err := legacyLabChange(httptest.NewRecorder(), r)
	if err != nil || got.Reason != "Проверено" {
		t.Fatal("reason normalization", err)
	}
}

func TestLegacyLabRejectsInvalidRoutesWithoutDatabase(t *testing.T) {
	for _, path := range []string{"/me/legacy-lab", "/api/v1/me/legacy-lab/", "/api/v1//me/legacy-lab"} {
		w := httptest.NewRecorder()
		(&Server{}).legacyLab(w, httptest.NewRequest("GET", path, nil), identity.Principal{}, []string{"me", "legacy-lab"})
		if w.Code != 404 {
			t.Fatal("noncanonical URL reached database", path)
		}
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"PUT", "me/legacy-lab", 404}, {"POST", "admin/users/id/legacy-lab", 404},
		{"GET", "me/legacy-lab/extra", 404}, {"GET", "admin/users/id/legacy-lab/extra", 404},
		{"GET", "admin/users/not-uuid/legacy-lab", 400}, {"GET", "admin/users/00000000-0000-0000-0000-000000000000/legacy-lab", 400},
		{"GET", "me/legacy-lab?user_id=forged", 400}, {"GET", "me/legacy-lab?x=1&x=2", 400},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/"+tc.path, nil)
		w := httptest.NewRecorder()
		(&Server{}).legacyLab(w, r, identity.Principal{}, strings.Split(strings.Split(tc.path, "?")[0], "/"))
		if w.Code != tc.status {
			t.Fatalf("%s %s: got%d", tc.method, tc.path, w.Code)
		}
	}
}
