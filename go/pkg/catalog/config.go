package catalog

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the config-file-driven source for a Catalog, loaded instead of a
// live etcd read per the Phase 1 scope decision (issue #9). Its shape is
// deliberately a subset of DYNAMOS's own
// configuration/etcd_launch_files/{agreements,datasets}.json — one party's
// Agreement worth of Relations, plus the pb.Dataset fields those Relations
// reference — rather than a new format. Swapping this loader for a live
// etcd-backed source later should only require a new LoadConfig-equivalent;
// Catalog/Dataset/BuildCatalog do not know where a Config came from.
type Config struct {
	Party         string                    `json:"party"`
	AgentEndpoint string                    `json:"agentEndpoint"`
	Datasets      []DatasetConfig           `json:"datasets"`
	Relations     map[string]RelationConfig `json:"relations"`
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

// RelationConfig mirrors the Relation struct (go/pkg/api/http.go), keyed by
// counterparty identity in Config.Relations, same as Agreement.relations.
type RelationConfig struct {
	ID                      string   `json:"id"`
	RequestTypes            []string `json:"requestTypes"`
	DataSets                []string `json:"dataSets"`
	AllowedArchetypes       []string `json:"allowedArchetypes"`
	AllowedComputeProviders []string `json:"allowedComputeProviders"`
}

// LoadConfig reads and parses a catalog config file from disk.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading catalog config %q: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing catalog config %q: %w", path, err)
	}

	return &cfg, nil
}
