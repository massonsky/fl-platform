package adapters

import (
	"context"
	"errors"
	"testing"
)

func TestExportDerivedBudgetAndCancellation(t *testing.T) {
	files := map[string][]byte{"metrics.json": []byte(`[{"round":1,"split":"validation_global","name":"accuracy","value":0.875}]`)}
	err := exportDerived(context.Background(), files, 20)
	var f Failure
	if !errors.As(err, &f) || f.Code != "EXPORT_LIMIT_EXCEEDED" || files["metrics.csv"] != nil || files["validation_accuracy.svg"] != nil {
		t.Fatal("derived metadata was truncated or ignored budget", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = exportDerived(ctx, files, ExportMetadataLimit); !errors.Is(err, context.Canceled) {
		t.Fatal("derived metadata ignored cancellation", err)
	}
	remaining := 3
	buffer := exportBuffer{remaining: &remaining}
	if n, err := buffer.Write([]byte("four")); n != 0 || err == nil || buffer.Len() != 0 || remaining != 3 {
		t.Fatal("overflow mutated budget or buffer")
	}
}
