package earnwallet

// The generator version is pinned here and nowhere else, so a local
// regeneration and the one CI checks cannot produce different files.
//
// openapi.yaml is not edited here either: the service exports it with
// `chainwallet openapi --out openapi.yaml`.

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 --config oapi-codegen.yaml openapi.yaml
