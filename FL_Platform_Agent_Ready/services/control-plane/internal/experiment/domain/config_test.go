package domain

import (
	"math"
	"strings"
	"testing"
)

func valid() Config {
	h := strings.Repeat("a", 64)
	return Config{SchemaVersion: 1, DatasetID: "digits", DatasetHash: h, PartitionHash: h, ModelName: "mlp64-32-10", ModelRevision: "nir-v1", SchemaHash: h, InitialHash: h, RuntimeHash: h, Algorithm: "fedavg", Clients: 5, Rounds: 25, Seed: 11, Epochs: 3, Batch: 25, LR: 0.12, Optimizer: "sgd", Partition: "balanced", Selection: "all", Completion: Policy{Mode: "strict"}, RoundTimeout: 180, LeaseSeconds: 60}
}
func TestConfigurationBoundaries(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []func(*Config){func(c *Config) { c.Mu = .1 }, func(c *Config) { c.LR = math.NaN() }, func(c *Config) { c.Clients = 4 }, func(c *Config) { c.SchemaHash = "bad" }, func(c *Config) { c.LeaseSeconds = 181 }, func(c *Config) { c.Rounds = 0 }, func(c *Config) { c.Partition = "dirichlet" }, func(c *Config) { c.Completion = Policy{Mode: "quorum", MinClients: 6} }}
	for i, change := range cases {
		c := valid()
		change(&c)
		if c.Validate() == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	c := valid()
	c.Algorithm = "fedprox"
	if c.Validate() != nil {
		t.Fatal("FedProx mu=0 rejected")
	}
	c.Mu = .1
	c.Completion = Policy{Mode: "quorum", MinClients: 3}
	if c.Validate() != nil {
		t.Fatal("valid quorum rejected")
	}
}
