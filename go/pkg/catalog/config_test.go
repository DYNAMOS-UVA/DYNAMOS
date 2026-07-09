package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	err := os.WriteFile(path, []byte(`{
		"party": "VU",
		"agentEndpoint": "https://vu-agent.vu.svc.cluster.local/agent/v1/sqlDataRequest/vu-agent",
		"datasets": [
			{"name": "wageGap", "type": "csv", "delimiter": ";", "tables": ["Aanstellingen", "Personen"]}
		],
		"relations": {
			"jorrit.stutterheim@cloudnation.nl": {
				"id": "GUID",
				"requestTypes": ["sqlDataRequest", "genericRequest"],
				"dataSets": ["wageGap"],
				"allowedArchetypes": ["computeToData", "dataThroughTtp"],
				"allowedComputeProviders": ["SURF"]
			}
		}
	}`), 0644)
	require.NoError(t, err)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	assert.Equal(t, "VU", cfg.Party)
	require.Len(t, cfg.Datasets, 1)
	assert.Equal(t, "wageGap", cfg.Datasets[0].Name)
	assert.Equal(t, []string{"Aanstellingen", "Personen"}, cfg.Datasets[0].Tables)
	require.Contains(t, cfg.Relations, "jorrit.stutterheim@cloudnation.nl")
	assert.Equal(t, "GUID", cfg.Relations["jorrit.stutterheim@cloudnation.nl"].ID)
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/catalog.json")
	assert.Error(t, err)
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0644))

	_, err := LoadConfig(path)
	assert.Error(t, err)
}

// TestLoadConfig_ExampleFile guards against drift between this package and
// the real example config shipped with the dsp-connector service.
func TestLoadConfig_ExampleFile(t *testing.T) {
	cfg, err := LoadConfig("../../cmd/dsp-connector/config/example-catalog.json")
	require.NoError(t, err)

	assert.Equal(t, "VU", cfg.Party)
	require.Len(t, cfg.Datasets, 1)
	assert.Equal(t, "wageGap", cfg.Datasets[0].Name)
}
