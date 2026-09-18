// Package config is the single source of truth for what a service is.
//
// One file describes the service; every artifact - Dockerfile, Kubernetes
// manifests, Argo Application, CI - is derived from it. Adding a field here
// makes it available to every runtime at once.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
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

	// Module is the Go module path. Normally derived - from the repo for a
	// standalone service, from the repo plus the service's directory in a
	// monorepo - and set here only when the repo is not named after the
	// service.
	//
	// Go requires a module's path to match where it is fetched from, so
	// this is not a preference: get it wrong and `go get` fails for every
	// consumer. go-shlink-redirector is the case that needs it - the repo
	// carries a `go-` prefix the deployed service does not.
	Module string `yaml:"module,omitempty"`

	// Spec means this service generates its API from openapi.yml. On by
	// default; `spec: false` hand-writes server.go instead, which gets no
	// generated client, so consumers have nothing to import.
	Spec bool `yaml:"spec"`

	// Kind is the Kubernetes shape this service takes. Language and shape
	// are independent axes: a Go service and a Go cron job share every
	// build concern and no manifest concern, so `runtime` chooses how it
	// is built and `kind` chooses what it becomes.
	Kind string `yaml:"kind,omitempty"`

	// Schedule is required for kind: cronjob, in cron syntax.
	Schedule string `yaml:"schedule,omitempty"`

	// TimeZone the schedule is interpreted in. Without it Kubernetes uses
	// UTC, so `0 3 * * *` fires at 11pm the previous evening in ET - a
	// schedule that reads correctly and runs at the wrong time.
	TimeZone string `yaml:"timeZone,omitempty"`

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
	// On by default, via Defaults() rather than the zero value: Load
	// decodes the YAML over a seeded Config, so an omitted `hardened:`
	// keeps true and an explicit `hardened: false` overrides it. A
	// Config built in Go code should start from Defaults() for the same
	// reason - Complete() re-applies what it can, but the plain zero
	// value of this field is false.
	Hardened bool `yaml:"hardened"`

	// Metrics annotates the pod for Prometheus scraping. On by default:
	// alloy discovers pods by annotation, so a service without them
	// produces no metrics at all and the absence is silent.
	Metrics bool `yaml:"metrics"`

	// Patches adjust generated manifests without taking ownership of them.
	// Keyed by resource kind (Deployment, Service, CronJob, Ingress...),
	// each value is a YAML fragment strategically merged into that
	// resource. Supply only what differs: the generated base keeps
	// flowing, so later convention changes still reach this service.
	Patches map[string]string `yaml:"patches,omitempty"`

	// Overrides replace a generated file wholesale. This opts the service
	// OUT of every future convention change for that file - a new PSA
	// level, a probe timing fix, a securityContext tightening will all
	// silently skip it. Reach for `patches` first; this exists for the
	// cases a merge genuinely cannot express.
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
	VaultPath string `yaml:"vaultPath"`

	// Keys are the environment variables to inject, and the Vault
	// properties they come from.
	Keys []SecretKey `yaml:"keys"`
}

// SecretKey maps one environment variable to one property under the
// service's Vault path. It decodes from either form:
//
//	keys:
//	  - NTFY_TOKEN              # property: ntfy_token
//	  - SHLINK_API_KEY: api-key # property: api-key
//
// The bare form derives the property by lowercasing, which is right when
// the variable is named for what it is. The mapping form exists because
// that derivation is wrong whenever the variable repeats its own app
// name: SHLINK_API_KEY under vaultPath `shlink` would ask Vault for
// `shlink/shlink_api_key`, and the convention across this cluster is that
// the property does NOT repeat the path - `arr/api-keys` holds `sonarr`,
// `authelia/keys` holds `session_secret`.
//
// Getting it wrong is not a render-time error: the manifests apply
// cleanly and ESO then fails to sync a property that does not exist, so
// the pod starts without the variable it needs.
type SecretKey struct {
	Env      string // the environment variable inside the pod
	Property string // the property under VaultPath
}

// MarshalYAML writes back whichever form the key came from, so a file
// this tool writes is a file it can read.
//
// Without it, yaml.Marshal emits the struct - {Env: X, Property: y} -
// which UnmarshalYAML then rejects, because it accepts a string or a
// single-entry mapping and neither is that. The asymmetry is silent
// until something round-trips a config, and then it fails at load with
// an error about the file rather than about the code.
//
// No discriminator field is needed to remember the original form: the
// bare form means "property is the lowercased env var", so the rule that
// decodes it also decides how to encode it.
func (k SecretKey) MarshalYAML() (any, error) {
	if k.Property == strings.ToLower(k.Env) {
		return k.Env, nil
	}
	return map[string]string{k.Env: k.Property}, nil
}

// EnvKeys builds keys the conventional way, for a Config assembled in Go
// code rather than decoded from YAML.
func EnvKeys(envs ...string) []SecretKey {
	out := make([]SecretKey, 0, len(envs))
	for _, e := range envs {
		out = append(out, SecretKey{Env: e, Property: strings.ToLower(e)})
	}
	return out
}

// UnmarshalYAML accepts a bare string or a single-entry mapping.
func (k *SecretKey) UnmarshalYAML(value *yaml.Node) error {
	var env string
	if err := value.Decode(&env); err == nil {
		k.Env = env
		k.Property = strings.ToLower(env)
		return nil
	}

	var m map[string]string
	if err := value.Decode(&m); err != nil {
		return fmt.Errorf("a secrets key must be `NAME` or `NAME: vault-property`")
	}
	if len(m) != 1 {
		return fmt.Errorf("a secrets key mapping must have exactly one entry, got %d", len(m))
	}
	for env, prop := range m {
		k.Env, k.Property = env, prop
	}
	return nil
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

// Workload kinds. A service is long-running and gets a Service, probes and
// a rollout strategy; a cronjob runs to completion on a schedule and gets
// none of those.
const (
	KindService = "service"
	KindCronJob = "cronjob"
)

var validKinds = map[string]bool{KindService: true, KindCronJob: true}

// DefaultPort is what a service listens on when the config does not say.
// It matches the `port` default in the JSON schema and the `init` flag;
// all three must agree or a config is valid against one and not the other.
const DefaultPort = 3000

// DefaultProbePath is where readiness and liveness probes look. The
// go-service template serves it; a service that serves something else
// sets `probes.path`.
const DefaultProbePath = "/healthz"

// IsCronJob reports whether this service runs on a schedule rather than
// continuously.
func (c *Config) IsCronJob() bool { return c.Kind == KindCronJob }

var nameRE = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)

// Defaults is the Config a config.yaml is decoded ON TOP OF, so an
// omitted key keeps the value here rather than Go's zero value. That is
// the whole defaulting mechanism for constant defaults: `hardened:` left
// out stays true, `hardened: false` overrides it, and no custom
// UnmarshalYAML or tri-state pointer is involved.
//
// It is a function, not a var: it hands out *Probes and *Resources, and a
// shared var would let one Load mutate the defaults the next one sees.
//
// NEVER seed a map or a slice here. yaml.v3 MERGES a decoded map into an
// existing one rather than replacing it, so a seeded entry survives
// whatever the file says and cannot be removed from YAML at all. Seeded
// slices are replaced wholesale, which is a different surprise. Both
// belong in applyDefaults, where the behaviour is explicit.
//
// Port is deliberately absent: it depends on Kind, which is not known
// until the file is decoded. See applyDefaults.
func Defaults() Config {
	return Config{
		Kind:      KindService,
		Replicas:  1,
		Hardened:  true,
		Metrics:   true,
		Spec:      true,
		Probes:    &Probes{Path: DefaultProbePath},
		Resources: &Resources{CPURequest: "10m", MemoryRequest: "32Mi", MemoryLimit: "64Mi"},
	}
}

// Load reads and validates a config, applying defaults so that callers see
// a fully-populated struct rather than having to re-derive them.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Decoding over Defaults() is what makes an omitted key keep its
	// default instead of becoming a zero value.
	//
	// KnownFields turns a typo into an error rather than silence:
	// `hardend: false` would otherwise decode cleanly, change nothing,
	// and ship an unhardened deploy. This reaches NESTED keys too -
	// `ingress: {publik: true}` ships a LAN-only ingress when the author
	// asked for a public one, and that used to pass unnoticed.
	// Against the schema first, so the CLI enforces exactly what the
	// editor shows: the name pattern, replicas' minimum, port's range,
	// the kind enum. Before decoding, because defaulting erases the
	// difference between an omitted field and a rejected one.
	if err := validateAgainstSchema(b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	c := Defaults()
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	// An empty file is not a parse failure, it is a config that omits
	// everything - which then fails validation with "name is required"
	// and the rest, naming what to fix. Decode reports that as io.EOF.
	if err := d.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Complete(); err != nil {
		return nil, err
	}
	return &c, nil
}

// applyDefaults covers what Defaults() cannot: values derived from other
// fields, and the nil pointers a Config built in Go code still arrives
// with. Constant defaults live in Defaults(), not here.
func (c *Config) applyDefaults() {
	if c.Kind == "" {
		c.Kind = KindService
	}
	// Derived from another field, so no literal can express it.
	if c.Namespace == "" {
		c.Namespace = c.Name
	}
	// Defaults() seeds this to 1, so a config.yaml that omits `replicas`
	// arrives here already at 1 and this branch does not fire. It exists
	// for a Config built in Go code, where 0 really does mean "unset" -
	// there is no file, so there is nothing to have said 0 deliberately.
	//
	// The consequence worth knowing: `replicas: 0` in a file is coerced
	// to 1 rather than scaling the service to zero. The schema says
	// minimum 1, so an editor flags it, and Validate rejects a negative.
	// Supporting a real zero would mean making this *int - which is the
	// tri-state this package deliberately does not use.
	if c.Replicas == 0 {
		c.Replicas = 1
	}
	// Conditional on Kind, so it cannot be seeded either: a cronjob has
	// no Service and no port, and render keys off Port == 0 to decide
	// whether to annotate the pod for scraping. Seeding a port
	// unconditionally would point Alloy at a port cronjobs never listen
	// on.
	//
	// A service without a port rendered containerPort: 0, targetPort: 0
	// and probes against port 0: manifests that apply cleanly and describe
	// a pod that can never pass a readiness check.
	if c.Port == 0 && !c.IsCronJob() {
		c.Port = DefaultPort
	}
	// Still needed after Defaults(): an explicit `probes:` with no value
	// decodes as null and nils the seeded pointer, and a Config built in
	// Go code never passed through Defaults() at all.
	if c.Probes == nil {
		c.Probes = &Probes{Path: DefaultProbePath}
	} else if c.Probes.Path == "" {
		c.Probes.Path = DefaultProbePath
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

// Complete fills in defaults and then validates, which is what every
// caller of a freshly built or freshly parsed Config wants. It is
// separate from Validate because the two are different jobs: one writes
// to the Config, the other only reads it. Fusing them meant a method
// named for a question performed a mutation - and mutated even when it
// returned an error, so a caller that validated, saw a failure, fixed one
// field and validated again was working on a half-defaulted struct.
func (c *Config) Complete() error {
	c.applyDefaults()
	return c.Validate()
}

// Validate reports every problem at once rather than the first, so a
// misconfigured service is fixed in one pass instead of N deploys.
//
// It does not modify the Config. Call Complete on one built in code or
// parsed from YAML; validating a Config that has not been defaulted will
// report the missing defaults as errors, which is the honest answer.
func (c Config) Validate() error {
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
	if c.Replicas < 0 {
		add("replicas cannot be negative; got %d", c.Replicas)
	}
	if c.Kind != "" && !validKinds[c.Kind] {
		add("kind %q is not one of: service, cronjob", c.Kind)
	}
	if c.IsCronJob() {
		if c.Schedule == "" {
			add("schedule is required for kind: cronjob")
		}
		if c.Ingress != nil {
			add("kind: cronjob cannot have an ingress; a job that exits serves no traffic")
		}
	} else if c.Schedule != "" {
		add("schedule is only meaningful for kind: cronjob")
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
