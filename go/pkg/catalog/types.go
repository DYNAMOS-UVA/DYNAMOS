// Package catalog builds DSP-compliant Catalog documents (DCAT Datasets
// carrying ODRL Offers) from DYNAMOS's own configuration, per the mapping
// designed in docs/catalog/dynamos-data-inventory.md and
// docs/catalog/dynamos-catalog-schema.md.
package catalog

// Context is the JSON-LD @context every document in this package is
// serialized with. docs/catalog/spec-reference/common/context-schema.json
// (vendored from the DSP TCK) requires @context to be an array of strings -
// an inline term-mapping object (as previously used here for the "dynamos:"
// prefix) fails that schema. The "dynamos:" vocabulary base URI is already a
// documented placeholder (docs/catalog/dynamos-catalog-schema.md) pending a
// real, resolvable namespace; until then "dynamos:"-prefixed values below
// are left as plain compact strings rather than formally bound terms.
var Context = []interface{}{
	"https://w3id.org/dspace/2025/1/context.jsonld",
}

// dynamosAccessFormat identifies DYNAMOS's own access mechanism (a request
// through the agent, never a raw file download) as the Distribution format.
// Fixed to match the worked example in docs/catalog/dynamos-catalog-example.jsonld.
const dynamosAccessFormat = "dynamos:sqlDataRequest"

type Catalog struct {
	Context       []interface{} `json:"@context"`
	ID            string        `json:"@id"`
	Type          string        `json:"@type"`
	ParticipantID string        `json:"participantId"`
	Service       []DataService `json:"service"`
	Dataset       []Dataset     `json:"dataset"`
}

type DataService struct {
	ID          string `json:"@id"`
	Type        string `json:"@type"`
	EndpointURL string `json:"endpointURL"`
}

type Dataset struct {
	ID           string         `json:"@id"`
	Type         string         `json:"@type"`
	HasPolicy    []Offer        `json:"hasPolicy"`
	Distribution []Distribution `json:"distribution"`
}

// RootDataset is a Dataset served as a standalone document - the ack for the
// Dataset Request Message (GET /catalog/datasets/:id). Per dataset-schema.json,
// only a *root* Dataset carries its own @context; a Dataset nested inside a
// Catalog's "dataset" array does not.
type RootDataset struct {
	Context []interface{} `json:"@context"`
	Dataset
}

type Offer struct {
	ID         string       `json:"@id"`
	Type       string       `json:"@type"`
	Assigner   string       `json:"assigner"`
	Assignee   string       `json:"assignee"`
	Permission []Permission `json:"permission"`
}

type Permission struct {
	Action     string       `json:"action"`
	Constraint []Constraint `json:"constraint"`
}

type Constraint struct {
	LeftOperand  string   `json:"leftOperand"`
	Operator     string   `json:"operator"`
	RightOperand []string `json:"rightOperand"`
}

type Distribution struct {
	Type          string `json:"@type"`
	Format        string `json:"format"`
	AccessService string `json:"accessService"`
	Table         string `json:"dynamos:table"`
	Delimiter     string `json:"dynamos:delimiter"`
}
