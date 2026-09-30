package validation

import (
	"embed"
	"fmt"
	"io"

	"github.com/kaptinlin/jsonschema"
)

//go:embed metaschemas/*/*.json metaschemas/*/meta/*.json
var metaSchemas embed.FS

// Exact resource URIs only: caller URLs are never converted to filesystem paths.
var metaSchemaPaths = map[string]string{
	"https://json-schema.org/draft/2019-09/schema":                 "metaschemas/2019-09/schema.json",
	"https://json-schema.org/draft/2019-09/meta/core":              "metaschemas/2019-09/meta/core.json",
	"https://json-schema.org/draft/2019-09/meta/applicator":        "metaschemas/2019-09/meta/applicator.json",
	"https://json-schema.org/draft/2019-09/meta/validation":        "metaschemas/2019-09/meta/validation.json",
	"https://json-schema.org/draft/2019-09/meta/meta-data":         "metaschemas/2019-09/meta/meta-data.json",
	"https://json-schema.org/draft/2019-09/meta/format":            "metaschemas/2019-09/meta/format.json",
	"https://json-schema.org/draft/2019-09/meta/content":           "metaschemas/2019-09/meta/content.json",
	"https://json-schema.org/draft/2020-12/schema":                 "metaschemas/2020-12/schema.json",
	"https://json-schema.org/draft/2020-12/meta/core":              "metaschemas/2020-12/meta/core.json",
	"https://json-schema.org/draft/2020-12/meta/applicator":        "metaschemas/2020-12/meta/applicator.json",
	"https://json-schema.org/draft/2020-12/meta/unevaluated":       "metaschemas/2020-12/meta/unevaluated.json",
	"https://json-schema.org/draft/2020-12/meta/validation":        "metaschemas/2020-12/meta/validation.json",
	"https://json-schema.org/draft/2020-12/meta/meta-data":         "metaschemas/2020-12/meta/meta-data.json",
	"https://json-schema.org/draft/2020-12/meta/format-annotation": "metaschemas/2020-12/meta/format-annotation.json",
	"https://json-schema.org/draft/2020-12/meta/format-assertion":  "metaschemas/2020-12/meta/format-assertion.json",
	"https://json-schema.org/draft/2020-12/meta/content":           "metaschemas/2020-12/meta/content.json",
}

func offlineCompiler() *jsonschema.Compiler {
	compiler := jsonschema.NewCompiler()
	clear(compiler.Loaders)
	compiler.RegisterLoader("https", loadMetaSchema)
	return compiler
}

func loadMetaSchema(uri string) (io.ReadCloser, error) {
	path, ok := metaSchemaPaths[uri]
	if !ok {
		return nil, fmt.Errorf("external schema resource is unavailable offline: %q", uri)
	}
	return metaSchemas.Open(path)
}
