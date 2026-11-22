package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	ExportModelLimit    = 32 << 20
	ExportMetadataLimit = 8 << 20
)

type ExportBlobs interface {
	Get(context.Context, string, string, int64) ([]byte, error)
}

// Export captures completed-run metadata under authorization, then releases the
// transaction before external IO. Callers must reauthorize before delivery.
func (s Store) Export(ctx context.Context, access Access, run string) (map[string][]byte, error) {
	snapshot, err := s.exportSnapshot(ctx, access, run)
	if err != nil {
		return nil, err
	}
	if err = exportDerived(ctx, snapshot.files, snapshot.remaining); err != nil {
		return nil, accessFailure(err)
	}
	if s.Blobs == nil {
		return nil, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	weights, err := s.Blobs.Get(ctx, snapshot.key, snapshot.hash, snapshot.size)
	if err != nil {
		return nil, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	hash := sha256.Sum256(weights)
	if int64(len(weights)) != snapshot.size || hex.EncodeToString(hash[:]) != snapshot.hash {
		return nil, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	if err = ctx.Err(); err != nil {
		return nil, accessFailure(err)
	}
	snapshot.files["final_model.bin"] = weights
	return snapshot.files, nil
}

// exportBuffer refuses the whole overflowing write. A failed partial buffer is
// never returned as an export, and derived files share the metadata budget.
type exportBuffer struct {
	bytes.Buffer
	remaining *int
}

func (b *exportBuffer) Write(p []byte) (int, error) {
	if len(p) > *b.remaining {
		return 0, Failure{409, "EXPORT_LIMIT_EXCEEDED"}
	}
	*b.remaining -= len(p)
	return b.Buffer.Write(p)
}

func exportDerived(ctx context.Context, files map[string][]byte, remaining int) error {
	var metrics []struct {
		Round int     `json:"round"`
		Split string  `json:"split"`
		Name  string  `json:"name"`
		Value float64 `json:"value"`
	}
	if err := json.Unmarshal(files["metrics.json"], &metrics); err != nil {
		return err
	}
	output, svg := exportBuffer{remaining: &remaining}, exportBuffer{remaining: &remaining}
	writer := csv.NewWriter(&output)
	if err := writer.Write([]string{"round", "split", "name", "value"}); err != nil {
		return err
	}
	if _, err := fmt.Fprint(&svg, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 300"><rect width="800" height="300" fill="white"/><text x="20" y="20">Validation accuracy by round</text><polyline points="`); err != nil {
		return err
	}
	maximumRound := 1
	for _, m := range metrics {
		maximumRound = max(maximumRound, m.Round)
	}
	for _, m := range metrics {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writer.Write([]string{fmt.Sprint(m.Round), m.Split, m.Name, fmt.Sprintf("%.17g", m.Value)}); err != nil {
			return err
		}
		if m.Split == "validation_global" && m.Name == "accuracy" {
			if _, err := fmt.Fprintf(&svg, "%.3f,%.3f ", 30+float64(m.Round-1)/float64(max(1, maximumRound-1))*740, 270-m.Value*240); err != nil {
				return err
			}
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	if _, err := fmt.Fprint(&svg, `" fill="none" stroke="#2563eb" stroke-width="2"/></svg>`); err != nil {
		return err
	}
	files["metrics.csv"], files["validation_accuracy.svg"] = output.Bytes(), svg.Bytes()
	return nil
}
