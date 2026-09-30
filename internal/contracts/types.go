// Package contracts provides independent semantic operations over a prepared
// contract set. It validates generated structure, but does not define proposal
// application semantics, including omitted fields.
package contracts

import (
	"encoding/json"

	"github.com/vectorphresh/0001-grodt/internal/openai"
)

type Definition struct{ Contracts []Contract }

// Contract describes a caller-defined portion of state. ModelWritable controls
// only whether its schema can be offered for proposal generation.
type Contract struct {
	Name          string
	Description   string
	Schema        json.RawMessage
	ModelWritable bool
}

// Input contains opaque caller data. Context is optional; absent context is null.
type Input struct {
	State       json.RawMessage
	Information json.RawMessage
	Context     json.RawMessage
}

// Selection retains accounting even when an invoked operation fails. Usage is
// the known subtotal; UsageRequests reports how many calls supplied usage.
type Selection struct {
	Names         []string
	Usage         *openai.Usage
	Requests      uint64
	UsageRequests uint64
}

// Mutation is generated JSON, returned unchanged. It is not approved for state
// application. Requests counts Component 001 invocations, including failed ones.
// Usage is the known subtotal (nil when unavailable); UsageRequests reports
// coverage. An empty writable subset makes zero requests.
type Mutation struct {
	JSON          json.RawMessage
	Usage         *openai.Usage
	Requests      uint64
	UsageRequests uint64
}

// Reducer retains only an immutable contract definition and a client. Operations
// have no sequencing dependency and retain no execution state between calls.
type Reducer struct {
	client    openai.Client
	contracts []Contract
	byName    map[string]Contract
}
