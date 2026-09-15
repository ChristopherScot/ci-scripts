// Package config is the single source of truth for what a service is.
//
// One file describes the service; every artifact - Dockerfile, Kubernetes
// manifests, Argo Application, CI - is derived from it. Adding a field here
// makes it available to every runtime at once.
package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is a service's declared shape.
type Config struct {
	Name string `yaml:"name"`
	Team string `yaml:"team"`

	// Runtime selects the language/framework plugin that knows how to
	// build and containerise this service ("go", "node", ...). It is the
	// extension point: a new runtime is a new implementation, not a change
	// to this struct.
	Runtime string `yaml:"runtime"`

	Namespace string `yaml:"namespace,omitempty"`
	Replicas  int    `yaml:"replicas,omitempty"`

	Image     Image             `yaml:"image,omitempty"`
	Port      int               `yaml:"port,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Secrets   *Secrets          `yaml:"secrets,omitempty"`
	Ingress   *Ingress          `yaml:"ingress,omitempty"`
	Probes    *Probes           `yaml:"probes,omitempty"`
	Resources *Resources        `yaml:"resources,omitempty"`

	// Hardened applies a non-root, read-only-rootfs securityContext.
	//
	// Defaults to ON. A plain bool cannot tell "unset" from "false", so the
	// tri-state is confined to decoding: UnmarshalYAML flips this to
	// Disabled only when the YAML says so. Callers building a Config in
	// code get hardening without having to remember to ask, and render can
	// read it without a nil check or a validation precondition.
	HardeningDisabled bool `yaml:"-"`

	// MetricsDisabled turns off Prometheus scraping. On by default: alloy
	// discovers pods by annotation, so a service without them produces no
	// metrics at all and the absence is silent.
	MetricsDisabled bool `yaml:"-"`

	// Overrides replace a generated file wholesale, for the cases the
	// schema cannot express. Prefer widening the schema; this is the
	// escape hatch, not the first resort. The ImageURL placeholder is
	// substituted so an override still tracks the built image.
	Overrides map[string]string `yaml:"overrides,omitempty"`
}

type Image struct {
	// Registry path without a tag, e.g. ghcr.io/christopherscot/foo.
	Repository string `yaml:"repository,omitempty"`
}

// Secrets generates a SecretStore + ExternalSecret bound to this service's
// own Vault path, following the per-app policy convention: one role, one
// path, one namespace.
type Secrets struct {
	VaultPath string   `yaml:"vaultPath"`
	Keys      []string `yaml:"keys"`
}

type Ingress struct {
	Host string `yaml:"host"`
	// Public routes via the internet-facing controller; otherwise LAN-only.
	Public bool `yaml:"public,omitempty"`
	// Authelia puts forward-auth in front. Not available for public hosts
	// reached off-LAN, since the auth host resolves internally only.
	Authelia bool `yaml:"authelia,omitempty"`
}

type Probes struct {
	Path string `yaml:"path,omitempty"`
}

type Resources struct {
	CPURequest    string `yaml:"cpuRequest,omitempty"`
	MemoryRequest string `yaml:"memoryRequest,omitempty"`
	MemoryLimit   string `yaml:"memoryLimit,omitempty"`
}

// UnmarshalYAML defaults Hardened to true. Without this, a config that
// simply omits the field would decode to false and silently generate an
// unhardened deployment - the opposite of the intended default.
// knownTopLevelKeys is every field settable from YAML, including the two
// the decoder handles specially. Kept beside UnmarshalYAML because that is
// what a reader checks when a key is rejected.
var knownTopLevelKeys = map[string]bool{
	"name": true, "team": true, "runtime": true, "namespace": true,
	"replicas": true, "port": true, "image": true, "env": true,
	"secrets": true, "ingress": true, "probes": true, "resources": true,
	"overrides": true, "hardened": true, "metrics": true,
}

func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	// yaml.Decoder.KnownFields does not reach inside a custom
	// UnmarshalYAML, so an unknown key would decode silently - and
	// `hardend: false` doing nothing is the exact case the schema exists
	// to catch. Check the keys directly.
	var unknown []string
	for i := 0; i+1 < len(value.Content); i += 2 {
		if k := value.Content[i].Value; !knownTopLevelKeys[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown field(s): %s", strings.Join(unknown, ", "))
	}

	type plain Config // avoid recursing into this method
	var tmp plain
	if err := value.Decode(&tmp); err != nil {
		return err
	}
	*c = Config(tmp)

	// `hardened:` is absent from the struct tags above precisely so that an
	// omitted field cannot be read as false. Look for it explicitly.
	var probe struct {
		Hardened *bool `yaml:"hardened"`
	}
	if err := value.Decode(&probe); err != nil {
		return err
	}
	if probe.Hardened != nil && !*probe.Hardened {
		c.HardeningDisabled = true
	}

	var mprobe struct {
		Metrics *bool `yaml:"metrics"`
	}
	if err := value.Decode(&mprobe); err != nil {
		return err
	}
	if mprobe.Metrics != nil && !*mprobe.Metrics {
		c.MetricsDisabled = true
	}
	return nil
}

// Hardened reports whether the generated deployment should run non-root
// with a read-only root filesystem.
func (c *Config) Hardened() bool { return !c.HardeningDisabled }

// Metrics reports whether the pod should be annotated for Prometheus
// scraping.
func (c *Config) Metrics() bool { return !c.MetricsDisabled }

var nameRE = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// Load reads and validates a config, applying defaults so that callers see
// a fully-populated struct rather than having to re-derive them.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	// KnownFields so a typo is an error rather than silence: `hardend:
	// false` would otherwise decode cleanly, change nothing, and ship an
	// unhardened deploy - the exact case the schema was written for.
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, c.Validate()
}

func (c *Config) applyDefaults() {
	if c.Namespace == "" {
		c.Namespace = c.Name
	}
	if c.Replicas == 0 {
		c.Replicas = 1
	}
	if c.Probes == nil {
		c.Probes = &Probes{Path: "/healthz"}
	} else if c.Probes.Path == "" {
		c.Probes.Path = "/healthz"
	}
	if c.Resources == nil {
		c.Resources = &Resources{}
	}
	if c.Resources.CPURequest == "" {
		c.Resources.CPURequest = "10m"
	}
	if c.Resources.MemoryRequest == "" {
		c.Resources.MemoryRequest = "32Mi"
	}
	if c.Resources.MemoryLimit == "" {
		c.Resources.MemoryLimit = "64Mi"
	}
}

// Validate applies defaults and then reports every problem at once rather
// than the first, so a misconfigured service is fixed in one pass instead
// of N deploys. Defaults are applied here rather than only on load so that
// a Config built in code - by `init`, or by a test - is as complete as one
// read from disk.
func (c *Config) Validate() error {
	c.applyDefaults()
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

	if c.Name == "" {
		add("name is required")
	} else if !nameRE.MatchString(c.Name) {
		add("name %q must be a DNS label: lowercase alphanumeric and hyphens, starting with a letter", c.Name)
	}
	if c.Team == "" {
		add("team is required")
	}
	if c.Runtime == "" {
		add("runtime is required; see `homelabctl init --help` for the registered runtimes")
	}
	if c.Secrets != nil {
		if c.Secrets.VaultPath == "" {
			add("secrets.vaultPath is required when secrets is set")
		}
		if len(c.Secrets.Keys) == 0 {
			add("secrets.keys must list at least one key")
		}
	}
	if c.Ingress != nil {
		if c.Ingress.Host == "" {
			add("ingress.host is required when ingress is set")
		}
		// Authelia is LAN-only; pairing it with a public host produces an
		// endpoint that dead-ends off-network, which is invisible until
		// someone tries it from cellular.
		if c.Ingress.Public && c.Ingress.Authelia {
			add("ingress.authelia cannot be used with ingress.public: the auth host resolves on the LAN only, so off-LAN requests would dead-end")
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid config:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
