package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaptinlin/jsonschema"
)

const objectSchema = `{"type":"object","properties":{"symbol":{"enum":["AAPL","NVDA","MSFT"]},"quantity":{"type":"number"}},"required":["symbol","quantity"]}`

func validate(t *testing.T, schema, data string) Result {
	t.Helper()
	result, err := New().Validate(context.Background(), json.RawMessage(schema), json.RawMessage(data))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Walk the serialized native result tree, whose instance paths are relative
// to their parents. This helper does not interpret schemas.
func diagnostic(t *testing.T, raw json.RawMessage, path, keyword string) *jsonschema.EvaluationError {
	t.Helper()
	var root jsonschema.EvaluationResult
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	var find func(*jsonschema.EvaluationResult, string) *jsonschema.EvaluationError
	find = func(node *jsonschema.EvaluationResult, parent string) *jsonschema.EvaluationError {
		location := parent + node.InstanceLocation
		if location == path && node.Errors[keyword] != nil {
			return node.Errors[keyword]
		}
		for _, child := range node.Details {
			if found := find(child, location); found != nil {
				return found
			}
		}
		return nil
	}
	found := find(&root, "")
	if found == nil {
		t.Fatalf("missing %s diagnostic at %q: %s", keyword, path, raw)
	}
	if found.Message == "" || found.Code == "" || found.Keyword != keyword {
		t.Fatalf("incomplete diagnostic: %+v", found)
	}
	return found
}

func TestValidObject(t *testing.T) {
	result := validate(t, objectSchema, `{"symbol":"AAPL","quantity":6}`)
	if !result.Valid || len(result.Details) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestStructuredFailures(t *testing.T) {
	for _, tt := range []struct{ name, schema, data, path, keyword, parameter, contains string }{
		{"scalar type", objectSchema, `{"symbol":"AAPL","quantity":"six"}`, "/quantity", "type", "expected", "number"},
		{"required", objectSchema, `{"symbol":"AAPL"}`, "", "required", "property", "quantity"},
		{"enum", objectSchema, `{"symbol":"OTHER","quantity":6}`, "/symbol", "enum", "expected", "AAPL"},
		{"nested", `{"type":"object","properties":{"account":{"type":"object","properties":{"position":` + objectSchema + `}}}}`, `{"account":{"position":{"symbol":"AAPL","quantity":"six"}}}`, "/account/position/quantity", "type", "expected", "number"},
		{"array member", `{"type":"array","items":` + objectSchema + `}`, `[{"symbol":"AAPL","quantity":6},{"symbol":"NVDA","quantity":"oops"},{"symbol":"MSFT","quantity":3}]`, "/1/quantity", "type", "expected", "number"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := validate(t, tt.schema, tt.data)
			if result.Valid {
				t.Fatal("invalid data accepted")
			}
			err := diagnostic(t, result.Details, tt.path, tt.keyword)
			if !strings.Contains(fmt.Sprint(err.Params[tt.parameter]), tt.contains) {
				t.Fatalf("parameters = %v", err.Params)
			}
			encoded, e := json.Marshal(result)
			if e != nil || !bytes.Contains(encoded, []byte(`"details":{`)) {
				t.Fatalf("feedback is not structured JSON: %s, %v", encoded, e)
			}
		})
	}
}

func TestMultipleFailures(t *testing.T) {
	result := validate(t, objectSchema, `{"symbol":"OTHER","quantity":"six"}`)
	if result.Valid {
		t.Fatal("invalid data accepted")
	}
	diagnostic(t, result.Details, "/symbol", "enum")
	diagnostic(t, result.Details, "/quantity", "type")
}

func TestSchemaFailures(t *testing.T) {
	for _, schema := range []string{
		`{`, `null`, `[]`, `42`, `{"type":"not-a-type"}`,
		`{"required":["symbol","symbol"]}`, `{"minLength":-1}`,
		`{"properties":{"x":{"type":"not-a-type"}}}`,
		`{"type":"string","pattern":"["}`, `{"$ref":"#/$defs/missing"}`,
	} {
		t.Run(schema, func(t *testing.T) {
			result, err := New().Validate(context.Background(), json.RawMessage(schema), json.RawMessage(`{}`))
			var schemaErr *SchemaError
			if !errors.As(err, &schemaErr) || result.Valid || len(result.Details) != 0 {
				t.Fatalf("result=%+v, err=%v", result, err)
			}
			if schemaErr.Unwrap() == nil {
				t.Fatal("missing cause")
			}
		})
	}
	_, err := New().Validate(context.Background(), json.RawMessage(`{"required":["x","x"]}`), json.RawMessage(`{}`))
	var schemaErr *SchemaError
	if !errors.As(err, &schemaErr) || !json.Valid(schemaErr.Details) {
		t.Fatalf("missing structured schema feedback: %v", err)
	}
	diagnostic(t, schemaErr.Details, "/required", "uniqueItems")
}

func TestMalformedData(t *testing.T) {
	for _, data := range []string{"", "{", `{} {}`, `{"quantity":}`} {
		result := validate(t, `true`, data)
		if result.Valid {
			t.Fatalf("accepted %q", data)
		}
		if err := diagnostic(t, result.Details, "", "format"); err.Code != "invalid_json" {
			t.Fatalf("unexpected error: %+v", err)
		}
	}
}

func TestLocalReferencesAndBooleanSchemas(t *testing.T) {
	for _, tt := range []struct {
		schema, data string
		valid        bool
	}{
		{`{"$defs":{"number":{"type":"number"}},"$ref":"#/$defs/number"}`, "6", true},
		{`{"$defs":{"number":{"type":"number"}},"$ref":"#/$defs/number"}`, `"six"`, false},
		{`true`, `{"anything":[1,null]}`, true},
		{`false`, "null", false},
	} {
		if result := validate(t, tt.schema, tt.data); result.Valid != tt.valid {
			t.Fatalf("schema=%s: %+v", tt.schema, result)
		}
	}
}

func TestOfflineDialects(t *testing.T) {
	for _, dialect := range []string{
		"http://json-schema.org/draft-04/schema#",
		"http://json-schema.org/draft-06/schema#",
		"http://json-schema.org/draft-07/schema#",
		"https://json-schema.org/draft/2019-09/schema",
		"https://json-schema.org/draft/2020-12/schema",
	} {
		t.Run(dialect, func(t *testing.T) {
			schema := fmt.Sprintf(`{"$schema":%q,"type":"number"}`, dialect)
			if !validate(t, schema, "6").Valid {
				t.Fatal("valid number rejected")
			}
			if validate(t, schema, `"six"`).Valid {
				t.Fatal("invalid number accepted")
			}
			_, err := New().Validate(context.Background(), json.RawMessage(fmt.Sprintf(`{"$schema":%q,"required":["x","x"]}`, dialect)), json.RawMessage("{}"))
			var schemaErr *SchemaError
			if !errors.As(err, &schemaErr) || len(schemaErr.Details) == 0 {
				t.Fatalf("invalid schema accepted: %v", err)
			}
		})
	}
}

func TestRemoteReferencesNeverFetch(t *testing.T) {
	var requests atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `true`)
	})
	plain := httptest.NewServer(handler)
	defer plain.Close()
	tls := httptest.NewTLSServer(handler)
	defer tls.Close()
	for _, uri := range []string{plain.URL + "/schema", tls.URL + "/schema", "file:///tmp/schema.json"} {
		for _, template := range []string{
			`{"$ref":%q}`,
			`{"anyOf":[true,{"$ref":%q}]}`,
			`{"$defs":{"unused":{"$ref":%q}}}`,
			`{"$schema":%q}`,
		} {
			_, err := New().Validate(context.Background(), json.RawMessage(fmt.Sprintf(template, uri)), json.RawMessage("{}"))
			var schemaErr *SchemaError
			if !errors.As(err, &schemaErr) {
				t.Fatalf("remote schema accepted: %s, %v", uri, err)
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("made %d network requests", requests.Load())
	}
}

func TestExactMetaSchemaResolution(t *testing.T) {
	for uri := range metaSchemaPaths {
		body, err := loadMetaSchema(uri)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			ID string `json:"$id"`
		}
		err = json.NewDecoder(body).Decode(&doc)
		body.Close()
		if err != nil || doc.ID != uri {
			t.Fatalf("resource mismatch: %s, %+v, %v", uri, doc, err)
		}
	}
	for _, uri := range []string{
		"https://json-schema.org/draft/2020-12/schema?other",
		"https://json-schema.org.evil/draft/2020-12/schema",
		"https://json-schema.org/draft/2020-12/../schema",
		"http://json-schema.org/draft/2020-12/schema",
	} {
		if body, err := loadMetaSchema(uri); err == nil {
			body.Close()
			t.Fatalf("unexpected resource: %s", uri)
		}
	}
}

func TestInputsRemainUnchanged(t *testing.T) {
	schema := json.RawMessage(objectSchema)
	data := json.RawMessage(` [{"symbol":"AAPL","quantity":6},{"symbol":"NVDA","quantity":"oops"}] `)
	beforeSchema, beforeData := bytes.Clone(schema), bytes.Clone(data)
	if _, err := New().Validate(context.Background(), schema, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(schema, beforeSchema) || !bytes.Equal(data, beforeData) {
		t.Fatal("input mutated")
	}
}

func TestContext(t *testing.T) {
	if _, err := New().Validate(nil, nil, nil); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := New().Validate(ctx, json.RawMessage(`true`), json.RawMessage(`null`))
	if !errors.Is(err, context.Canceled) || result.Valid {
		t.Fatalf("result=%+v, err=%v", result, err)
	}
}
