package config

// Schema is the JSON Schema for homelab.yaml.
//
// Generated files reference it with a `# yaml-language-server: $schema=`
// line, matching the convention already used by the arr-stack values
// files, so editors offer completion and flag unknown keys as you type.
// That matters here because the YAML decoder silently ignores a key it
// does not recognise: `hardend: false` is accepted and does nothing, and
// the result is an unhardened deploy nobody asked for.
//
// Hand-maintained alongside the Config struct rather than reflected at
// runtime - the struct has custom unmarshalling and a few fields whose
// YAML name differs from the field name, so a reflected schema would be
// wrong in exactly the places that matter. SchemaCoversConfig in the tests
// fails if a yaml-tagged field is missing here.
const Schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "homelab service",
  "type": "object",
  "required": ["name", "team", "runtime"],
  "additionalProperties": false,
  "properties": {
    "name": {
      "type": "string",
      "pattern": "^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$",
      "description": "DNS label: lowercase letters, digits and hyphens, starting with a letter."
    },
    "team": { "type": "string", "description": "Owning team; becomes a label on every resource." },
    "runtime": {
      "type": "string",
      "description": "Which language/shape plugin builds this. See homelabctl init --help.",
      "examples": ["go-service", "node-service", "go-cli"]
    },
    "namespace": { "type": "string", "description": "Defaults to the service name." },
    "replicas": { "type": "integer", "minimum": 1, "default": 1 },
    "port": { "type": "integer", "minimum": 1, "maximum": 65535, "default": 3000 },
    "hardened": {
      "type": "boolean",
      "default": true,
      "description": "Run non-root with a read-only root filesystem. Leave on unless the image cannot."
    },
    "metrics": {
      "type": "boolean",
      "default": true,
      "description": "Annotate the pod for Prometheus scraping. The service must serve /metrics."
    },
    "image": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "repository": { "type": "string", "description": "Registry path without a tag." }
      }
    },
    "env": {
      "type": "object",
      "additionalProperties": { "type": "string" },
      "description": "Plain environment variables. Secrets belong under 'secrets'."
    },
    "secrets": {
      "type": "object",
      "required": ["vaultPath", "keys"],
      "additionalProperties": false,
      "description": "Generates a SecretStore and ExternalSecret bound to this service's own Vault path.",
      "properties": {
        "vaultPath": { "type": "string", "description": "Path under kv, e.g. myservice/config." },
        "keys": { "type": "array", "minItems": 1, "items": { "type": "string" } }
      }
    },
    "ingress": {
      "type": "object",
      "required": ["host"],
      "additionalProperties": false,
      "properties": {
        "host": { "type": "string" },
        "public": {
          "type": "boolean",
          "default": false,
          "description": "Route via the internet-facing controller rather than LAN-only."
        },
        "authelia": {
          "type": "boolean",
          "default": false,
          "description": "Forward-auth. Cannot be combined with public: the auth host resolves on the LAN only."
        }
      }
    },
    "probes": {
      "type": "object",
      "additionalProperties": false,
      "properties": { "path": { "type": "string", "default": "/healthz" } }
    },
    "resources": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "cpuRequest": { "type": "string", "default": "10m" },
        "memoryRequest": { "type": "string", "default": "32Mi" },
        "memoryLimit": { "type": "string", "default": "64Mi" }
      }
    },
    "overrides": {
      "type": "object",
      "additionalProperties": { "type": "string" },
      "description": "Replace a generated file wholesale. Prefer widening the schema; {{ .ImageURL }} is substituted."
    }
  }
}
`
