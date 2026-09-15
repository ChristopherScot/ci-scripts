package config

// The names a service is known by, in one place.
//
// A service's name determines its namespace, its ServiceAccount, its Vault
// role and policy, the Secret its credentials land in, and its Argo
// Application. That is ONE design decision, and it used to be re-derived
// independently in render, vault and preflight - so changing the
// convention meant finding every `c.Name` that happened to mean
// "ServiceAccount" rather than "app label", across three packages, on the
// security boundary.
//
// These accessors are the single source. If a name needs to change shape,
// it changes here.

// ServiceAccountName is the identity the pod runs as, and the identity
// Vault's role binds to. Empty when the service reads no secrets, because
// the deployment then has no reason to leave the default account.
func (c *Config) ServiceAccountName() string {
	if c.Secrets == nil {
		return ""
	}
	return c.Name
}

// VaultRoleName is the Kubernetes auth role a service assumes.
func (c *Config) VaultRoleName() string { return c.Name }

// VaultPolicyName is the policy that role carries.
func (c *Config) VaultPolicyName() string { return c.Name }

// SecretStoreName is the ESO SecretStore backing this service.
func (c *Config) SecretStoreName() string { return "vault-" + c.Name }

// SecretName is the Kubernetes Secret ESO syncs into, and the one the
// deployment mounts. Written in two places before; if this suffix changes,
// a mismatch means the pod mounts a Secret that does not exist.
func (c *Config) SecretName() string { return c.Name + "-secrets" }

// AppName is the Argo Application, which is also the directory the
// manifests live in within the GitOps repo.
func (c *Config) AppName() string { return c.Name }
