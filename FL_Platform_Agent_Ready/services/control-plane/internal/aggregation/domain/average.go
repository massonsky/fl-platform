package domain

import (
	"encoding/binary"
	"errors"
	"math"
)

const MaxModelBytes = 256 << 20

func Validate(data []byte, expected int) error {
	if expected <= 0 || expected > MaxModelBytes || len(data) != expected || len(data)%4 != 0 {
		return errors.New("MODEL_SIZE_MISMATCH")
	}
	for i := 0; i < len(data); i += 4 {
		value := math.Float32frombits(binary.LittleEndian.Uint32(data[i:]))
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("NON_FINITE_MODEL")
		}
	}
	return nil
}

// Accumulator consumes clients in stable ID order, using O(model_size) memory.
type Accumulator struct {
	sums  []float64
	count uint64
}

func New(size int) (*Accumulator, error) {
	if size <= 0 || size > MaxModelBytes || size%4 != 0 {
		return nil, errors.New("MODEL_SIZE_MISMATCH")
	}
	return &Accumulator{sums: make([]float64, size/4)}, nil
}
func (a *Accumulator) Add(data []byte, count uint64) error {
	if count == 0 || count > 1<<40 || a.count > 1<<50-count {
		return errors.New("INVALID_SAMPLE_COUNT")
	}
	if err := Validate(data, len(a.sums)*4); err != nil {
		return err
	}
	for i := range a.sums {
		a.sums[i] += float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))) * float64(count)
	}
	a.count += count
	return nil
}
func (a *Accumulator) Bytes() ([]byte, error) {
	if a.count == 0 {
		return nil, errors.New("EMPTY_AGGREGATION")
	}
	result := make([]byte, len(a.sums)*4)
	for i, value := range a.sums {
		binary.LittleEndian.PutUint32(result[i*4:], math.Float32bits(float32(value/float64(a.count))))
	}
	return result, Validate(result, len(result))
}
