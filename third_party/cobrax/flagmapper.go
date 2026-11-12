package cobrax

import (
	"github.com/feizaonet/cobrax/internal/bridge/flags"
)

// FlagSchemaMapper maps a pflag.Flag to a jsonschema.Schema for a custom flag
// type. Return handled=true to use the returned schema as-is; return
// handled=false to fall back to cobrax's built-in flag type mapping.
//
// This lets applications register custom mappings for pflag.Value types that
// cobrax does not recognise, or override the built-in mapping for a standard
// type. The schema is a google/jsonschema-go jsonschema.Schema, the same type
// used throughout cobrax's schema generation.
type FlagSchemaMapper = flags.FlagSchemaMapper

// RegisterFlagSchemaMapper registers a custom schema mapper for the given flag
// type name (the value returned by flag.Value.Type()). A mapper registered for
// a built-in type overrides the built-in mapping for that type; a mapper
// registered for an unknown type handles custom pflag.Value implementations.
//
// It must be called before NewMCPServer or Config-based tool registration
// (typically during package init or application startup). It is not safe for
// concurrent use with a running server.
//
// Example: map a custom "durationRange" flag type to a JSON Schema object.
//
//	cobrax.RegisterFlagSchemaMapper("durationRange", func(f *pflag.Flag) (*jsonschema.Schema, bool) {
//	    return &jsonschema.Schema{
//	        Type: "object",
//	        Properties: map[string]*jsonschema.Schema{
//	            "min": {Type: "string"},
//	            "max": {Type: "string"},
//	        },
//	        Description: f.Usage,
//	    }, true
//	})
func RegisterFlagSchemaMapper(typeName string, m FlagSchemaMapper) {
	flags.RegisterFlagSchemaMapper(typeName, m)
}
