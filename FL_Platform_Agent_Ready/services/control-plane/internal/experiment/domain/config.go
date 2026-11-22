package domain

import (
	"errors"
	"math"
	"regexp"
)

type Policy struct {
	Mode       string `json:"mode"`
	MinClients int    `json:"min_clients,omitempty"`
}
type Config struct {
	SchemaVersion int     `json:"schema_version"`
	DatasetID     string  `json:"dataset_id"`
	DatasetHash   string  `json:"dataset_manifest_hash"`
	PartitionHash string  `json:"partition_manifest_hash"`
	ModelName     string  `json:"model_name"`
	ModelRevision string  `json:"model_revision"`
	SchemaHash    string  `json:"model_schema_hash"`
	InitialHash   string  `json:"initial_weights_hash"`
	RuntimeHash   string  `json:"runtime_manifest_hash"`
	Algorithm     string  `json:"algorithm"`
	Clients       int     `json:"clients"`
	Rounds        int     `json:"rounds"`
	Seed          uint32  `json:"seed"`
	Epochs        int     `json:"local_epochs"`
	Batch         int     `json:"batch_size"`
	LR            float64 `json:"learning_rate"`
	Mu            float64 `json:"mu"`
	Optimizer     string  `json:"optimizer"`
	Partition     string  `json:"partition"`
	Selection     string  `json:"selection_strategy"`
	Completion    Policy  `json:"completion_policy"`
	RoundTimeout  int     `json:"round_timeout_seconds"`
	LeaseSeconds  int     `json:"lease_seconds"`
}

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (c Config) Validate() error {
	bad := errors.New("INVALID_CONFIG")
	if c.SchemaVersion != 1 || c.Clients != 5 || c.DatasetID != "digits" || c.ModelName != "mlp64-32-10" || c.ModelRevision != "nir-v1" || c.Optimizer != "sgd" || c.Selection != "all" {
		return bad
	}
	for _, h := range []string{c.DatasetHash, c.PartitionHash, c.SchemaHash, c.InitialHash, c.RuntimeHash} {
		if !hashPattern.MatchString(h) {
			return bad
		}
	}
	if c.Algorithm != "fedavg" && c.Algorithm != "fedprox" {
		return bad
	}
	if c.Partition != "balanced" && c.Partition != "moderate" && c.Partition != "strong" {
		return bad
	}
	if math.IsNaN(c.Mu) || math.IsInf(c.Mu, 0) || c.Mu < 0 || c.Algorithm == "fedavg" && c.Mu != 0 {
		return bad
	}
	if math.IsNaN(c.LR) || math.IsInf(c.LR, 0) || c.LR <= 0 || c.LR > 1000 {
		return bad
	}
	if c.Rounds < 1 || c.Rounds > 10000 || c.Epochs < 1 || c.Epochs > 1000 || c.Batch < 1 || c.Batch > 65536 {
		return bad
	}
	if c.RoundTimeout < 30 || c.RoundTimeout > 86400 || c.LeaseSeconds < 10 || c.LeaseSeconds > 3600 || c.LeaseSeconds > c.RoundTimeout {
		return bad
	}
	if c.Completion.Mode == "strict" {
		if c.Completion.MinClients != 0 {
			return bad
		}
	} else if c.Completion.Mode != "quorum" || c.Completion.MinClients < 1 || c.Completion.MinClients > 5 {
		return bad
	}
	return nil
}
