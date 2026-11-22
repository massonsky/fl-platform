package adapters

import "testing"

func TestCanonicalJSON(t *testing.T) {
	actual, err := Canonical([]byte(`{"z":1.0,"a":0.12}`))
	if err != nil || string(actual) != `{"a":0.12,"z":1}` {
		t.Fatalf("canonical mismatch %s %v", actual, err)
	}
	for _, raw := range []string{`{"a":1,"a":2}`, `{"x":NaN}`, `{"x":1e999}`, "{\"a\":\"\xff\"}"} {
		if _, err := Canonical([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	var input struct {
		Name string `json:"name"`
	}
	if Decode([]byte(`{"name":"x","owner_id":"other"}`), &input) == nil {
		t.Fatal("accepted unknown property")
	}
}
