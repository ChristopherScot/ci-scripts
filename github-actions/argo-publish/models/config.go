package models

type Config struct {
	Name               string         `yaml:"name"`
	Team               string         `yaml:"team"`
	Replicas           int            `yaml:"replicas"`
	Type               string         `yaml:"type"`
	Ingress            Ingress        `yaml:"ingress"`
	Config             map[string]any `yaml:"config"`
	ServiceOverride    *string        `yaml:"service_override,omitempty"`
	AppOverride        *string        `yaml:"app_override,omitempty"`
	DeploymentOverride *string        `yaml:"deployment_override,omitempty"`
}

type Ingress struct {
	Type     string            `yaml:"type"`
	Selector map[string]string `yaml:"selector,omitempty"`
	Ports    []Port            `yaml:"ports"`
}

type Port struct {
	Port       int  `yaml:"port"`
	TargetPort *int `yaml:"targetPort,omitempty"`
	NodePort   *int `yaml:"nodePort,omitempty"`
}

type Metadata struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels"`
}

type Spec struct {
	Ports    []Port            `yaml:"ports"`
	Selector map[string]string `yaml:"selector"`
	Type     string            `yaml:"type"`
}

type MatchLabels struct {
	App string `yaml:"app"`
}

type Container struct {
	Name  string `yaml:"name"`
	Image string `yaml:"image"`
	Ports []struct {
		ContainerPort int `yaml:"containerPort"`
	} `yaml:"ports"`
}
