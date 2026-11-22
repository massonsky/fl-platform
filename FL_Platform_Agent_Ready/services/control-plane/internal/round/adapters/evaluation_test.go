package adapters

import (
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	"math"
	"testing"
)

func TestEvaluationIntegrity(t *testing.T) {
	matrix := make([]uint32, 100)
	for i := 0; i < 10; i++ {
		matrix[i*10+i] = 25
	}
	accuracy, f1, err := ValidateConfusion(matrix, 250)
	if err != nil || accuracy != 1 || f1 != 1 {
		t.Fatal("perfect matrix rejected")
	}
	request := &pb.SubmitEvaluationRequest{ValidationConfusion: matrix, Metrics: []*pb.MetricValue{{Name: "accuracy", Value: 1, Split: "validation_global"}, {Name: "macro_f1", Value: 1, Split: "validation_global"}, {Name: "cross_entropy", Value: 0, Split: "validation_global"}}}
	if err = validateMetrics(request, false); err != nil {
		t.Fatal(err)
	}
	request.Metrics[0].Value = .9
	if validateMetrics(request, false) == nil {
		t.Fatal("inconsistent metric accepted")
	}
	request.Metrics[0].Value = math.NaN()
	if validateMetrics(request, false) == nil {
		t.Fatal("NaN accepted")
	}
	request.Metrics[0].Value = 1
	request.TestConfusion = matrix
	if validateMetrics(request, false) == nil {
		t.Fatal("early test accepted")
	}
	if _, _, err = ValidateConfusion(matrix, 547); err == nil {
		t.Fatal("incorrect sample total accepted")
	}
}
