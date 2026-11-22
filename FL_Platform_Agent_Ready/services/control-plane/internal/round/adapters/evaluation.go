package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	scheduler "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/scheduler/adapters"
	"math"
)

func ValidateConfusion(matrix []uint32, total uint64) (float64, float64, error) {
	if len(matrix) != 100 {
		return 0, 0, errors.New("INVALID_EVALUATION")
	}
	var count, correct uint64
	var f1 float64
	for i := 0; i < 10; i++ {
		var row, col uint64
		for j := 0; j < 10; j++ {
			row += uint64(matrix[i*10+j])
			col += uint64(matrix[j*10+i])
		}
		count += row
		correct += uint64(matrix[i*10+i])
		if row+col > 0 {
			f1 += 2 * float64(matrix[i*10+i]) / float64(row+col)
		}
	}
	if count != total {
		return 0, 0, errors.New("INVALID_EVALUATION")
	}
	return float64(correct) / float64(total), f1 / 10, nil
}
func validateMetrics(request *pb.SubmitEvaluationRequest, final bool) error {
	matrices := map[string][]uint32{"validation_global": request.ValidationConfusion}
	totals := map[string]uint64{"validation_global": 250, "test_global": 547}
	if final {
		matrices["test_global"] = request.TestConfusion
	} else if len(request.TestConfusion) != 0 {
		return errors.New("EARLY_TEST_EVALUATION")
	}
	expected := len(matrices) * 3
	if len(request.Metrics) != expected {
		return errors.New("INVALID_EVALUATION")
	}
	seen := map[string]bool{}
	for _, metric := range request.Metrics {
		key := metric.Split + "/" + metric.Name
		if seen[key] || math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0) {
			return errors.New("INVALID_EVALUATION")
		}
		seen[key] = true
		matrix, ok := matrices[metric.Split]
		if !ok {
			return errors.New("INVALID_EVALUATION")
		}
		accuracy, f1, err := ValidateConfusion(matrix, totals[metric.Split])
		if err != nil {
			return err
		}
		switch metric.Name {
		case "accuracy":
			if math.Abs(metric.Value-accuracy) > 1e-9 {
				return errors.New("INVALID_EVALUATION")
			}
		case "macro_f1":
			if math.Abs(metric.Value-f1) > 1e-9 {
				return errors.New("INVALID_EVALUATION")
			}
		case "cross_entropy":
			if metric.Value < 0 {
				return errors.New("INVALID_EVALUATION")
			}
		default:
			return errors.New("INVALID_EVALUATION")
		}
	}
	return nil
}
func (s Store) Evaluate(ctx context.Context, client string, request *pb.SubmitEvaluationRequest) (*pb.SubmitEvaluationResponse, error) {
	response := &pb.SubmitEvaluationResponse{}
	hash := RequestHash(request)
	err := db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		found, err := Receipt(ctx, tx, client, "evaluate", request.IdempotencyKey, hash, response)
		if err != nil || found {
			return err
		}
		l, err := scheduler.Lock(ctx, tx, client, request.TaskId, request.LeaseId, request.LeaseToken)
		if err != nil {
			return err
		}
		if l.Kind != "evaluate" || request.RunId != l.RunID || int(request.RoundNo) != l.Round || int(request.ModelVersion) != l.Round {
			return errors.New("STALE_VERSION")
		}
		if err = validateMetrics(request, l.Round == l.Config.Rounds); err != nil {
			return err
		}
		validation, _ := json.Marshal(request.ValidationConfusion)
		var test any
		if len(request.TestConfusion) > 0 {
			b, _ := json.Marshal(request.TestConfusion)
			test = b
		}
		_, err = tx.Exec(ctx, `INSERT INTO evaluations(round_id,task_id,model_version_id,validation_confusion,test_confusion) SELECT $1,$2,id,$3,$4 FROM model_versions WHERE run_id=$5 AND version_no=$6`, l.RoundID, request.TaskId, validation, test, l.RunID, l.Round)
		if err != nil {
			return err
		}
		for _, metric := range request.Metrics {
			if _, err = tx.Exec(ctx, "INSERT INTO metric_points(run_id,round_id,split,metric_name,metric_value) VALUES($1,$2,$3,$4,$5)", l.RunID, l.RoundID, metric.Split, metric.Name, metric.Value); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE tasks SET status='submitted' WHERE id=$1", request.TaskId); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE rounds SET status='completed' WHERE id=$1", l.RoundID); err != nil {
			return err
		}
		if l.Round == l.Config.Rounds {
			if _, err = tx.Exec(ctx, "UPDATE runs SET status='completed',finished_at=now() WHERE id=$1", l.RunID); err != nil {
				return err
			}
		}
		if err = Event(ctx, tx, l.RunID, l.RoundID, client, "round.completed"); err != nil {
			return err
		}
		response.Recorded = true
		return SaveReceipt(ctx, tx, client, "evaluate", request.IdempotencyKey, hash, response)
	})
	return response, err
}
