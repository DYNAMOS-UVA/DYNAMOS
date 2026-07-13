package catalog

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/DYNAMOS-UVA/DYNAMOS/pkg/api"
)

// Config is the config-file-driven source for a Catalog, loaded instead of a
// live etcd read per the Phase 1 scope decision (issue #9). Its shape is
// deliberately a subset of DYNAMOS's own
// configuration/etcd_launch_files/{agreements,datasets}.json — one party's
// Agreement worth of Relations, plus the pb.Dataset fields those Relations
// reference — rather than a new format. Swapping this loader for a live
// etcd-backed source later should only require a new LoadConfig-equivalent;
// Catalog/Dataset/BuildCatalog do not know where a Config came from.
//
// Relations uses api.Relation directly (not a package-local mirror struct) so
// catalog-service (issue #27) can plug in an etcd-fetched api.Agreement's
// Relations map without a conversion step.
type Config struct {
	Party         string                  `json:"party"`
	AgentEndpoint string                  `json:"agentEndpoint"`
	Datasets      []DatasetConfig         `json:"datasets"`
	Relations     map[string]api.Relation `json:"relations"`
}

// DatasetConfig mirrors the pb.Dataset proto fields (proto-files/etcd.proto).
// sensitive_columns is intentionally not represented here — see decision 3 in
// docs/catalog/dynamos-catalog-schema.md: it's an internal enforcement detail,
// not something the catalog exposes.
type DatasetConfig struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Delimiter string   `json:"delimiter"`
	Tables    []string `json:"tables"`
}

// LoadConfig reads, parses, and validates a catalog config file from disk.
// Validation catches referential errors (e.g. a Relation pointing at a
// dataset name that doesn't exist) at load time rather than leaving them to
// surface later inside BuildCatalog on the first matching request.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading catalog config %q: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing catalog config %q: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating catalog config %q: %w", path, err)
	}

	return &cfg, nil
}

// Validate checks referential integrity between Relations and Datasets: every
// name in a Relation's DataSets must correspond to an actual DatasetConfig.
// Basic required fields (Party, AgentEndpoint) are also checked so a
// half-filled-in config fails at load time, not wherever it's later found to
// be missing something.
func (cfg *Config) Validate() error {
	if cfg.Party == "" {
		return fmt.Errorf("config is missing required field: party")
	}
	if cfg.AgentEndpoint == "" {
		return fmt.Errorf("config is missing required field: agentEndpoint")
	}

	known := make(map[string]bool, len(cfg.Datasets))
	for _, d := range cfg.Datasets {
		if d.Name == "" {
			return fmt.Errorf("dataset entry is missing required field: name")
		}
		known[d.Name] = true
	}

	for participant, rel := range cfg.Relations {
		if len(rel.DataSets) == 0 {
			return fmt.Errorf("relation %q for participant %q has no dataSets", rel.ID, participant)
		}
		for _, dsName := range rel.DataSets {
			if !known[dsName] {
				return fmt.Errorf("relation %q for participant %q references unknown dataset %q", rel.ID, participant, dsName)
			}
		}
	}

	return nil
}
