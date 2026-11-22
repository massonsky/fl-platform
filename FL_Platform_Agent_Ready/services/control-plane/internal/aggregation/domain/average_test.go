package domain

import (
	"encoding/binary"
	"math"
	"testing"
)

func vector(values ...float32) []byte {
	b := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	return b
}
func TestWeightedAnswer(t *testing.T) {
	a, _ := New(8)
	if err := a.Add(vector(1, 3), 1); err != nil {
		t.Fatal(err)
	}
	if err := a.Add(vector(5, 7), 3); err != nil {
		t.Fatal(err)
	}
	got, err := a.Bytes()
	if err != nil || string(got) != string(vector(4, 6)) {
		t.Fatalf("wrong weighted result: %v", err)
	}
}
func TestRejectInvalid(t *testing.T) {
	for _, data := range [][]byte{nil, {1}, vector(float32(math.NaN()), 1), vector(float32(math.Inf(1)), 1)} {
		if Validate(data, 8) == nil {
			t.Fatal("accepted malformed model")
		}
	}
	a, _ := New(8)
	if a.Add(vector(1, 2), 0) == nil {
		t.Fatal("accepted zero samples")
	}
	if _, err := a.Bytes(); err == nil {
		t.Fatal("accepted empty aggregation")
	}
}
