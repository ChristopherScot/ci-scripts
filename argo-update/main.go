package main

import (
	_ "embed"
	"html/template"
	"log"
	"os"

	"github.com/ChristopherScot/ci-scripts/github-actions/argo-update/models"
	"gopkg.in/yaml.v2"
)

//go:embed templates/app.go.yaml
var appTemplate string

//go:embed templates/service.go.yaml
var serviceTemplate string

//go:embed templates/deployment.go.yaml
var deploymentTemplate string

func main() {

	if len(os.Args) < 3 {
		log.Fatal("Usage: go run main.go <config_file> <image_url> [namespace]")
	}

	config_file := os.Args[1]
	imageURL := os.Args[2]
	namespace := "default"

	if len(os.Args) > 3 {
		namespace = os.Args[3]
	}

	// Get config from yaml file
	configFile, err := os.ReadFile(config_file)
	if err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	var config models.Config
	err = yaml.Unmarshal(configFile, &config)
	if err != nil {
		log.Fatalf("Error parsing config file: %v", err)
	}

	createDeploymentYaml(config, namespace, imageURL)
	createServiceYaml(config, namespace)
	// TODO: as I figure out what my app structure looks like this will need to be passed in
	// or at least based on other config params. For now, just hardcoding it.
	createAppYaml(config, "app-of-apps/apps/")

}

func createDeploymentYaml(config models.Config, namespace string, imageURL string) {

	// create directory if it doesn't exist
	if _, err := os.Stat(config.Name); os.IsNotExist(err) {
		os.Mkdir(config.Name, 0755)
	}

	file, err := os.Create(config.Name + "/deployment.yaml")
	if err != nil {
		log.Fatalf("Error creating file: %v", err)
	}
	defer file.Close()

	if config.DeploymentOverride != nil {
		// Write the deployment override to the file
		_, err = file.WriteString(*config.DeploymentOverride)
		if err != nil {
			log.Fatalf("Error writing deployment override to file: %v", err)
		}
		return
	}

	// Parse the template
	tmpl, err := template.New("deployment").Parse(deploymentTemplate)
	if err != nil {
		log.Fatalf("Error parsing template file: %v", err)
	}

	// Define the data to pass to the template
	deploymentArgs := struct {
		AppName      string
		AppNamespace string
		Team         string
		Replicas     int
		ImageURL     string
		Ports        []models.Port
		Selector     map[string]string
	}{
		AppName:      config.Name,
		AppNamespace: namespace,
		Team:         config.Team,
		Replicas:     config.Replicas,
		ImageURL:     imageURL,
		Ports:        config.Ingress.Ports,
		Selector:     config.Ingress.Selector,
	}

	// Execute the template with the data
	err = tmpl.Execute(file, deploymentArgs)
	if err != nil {
		log.Fatalf("Error executing template: %v", err)
	}

}

func createServiceYaml(config models.Config, namespace string) {

	file, err := os.Create(config.Name + "/service.yaml")
	if err != nil {
		log.Fatalf("Error creating file: %v", err)
	}
	defer file.Close()

	if config.ServiceOverride != nil {
		// Write the service override to the file
		_, err = file.WriteString(*config.ServiceOverride)
		if err != nil {
			log.Fatalf("Error writing service override to file: %v", err)
		}
		return
	}

	// Parse the template
	tmpl, err := template.New("service").Parse(serviceTemplate)
	if err != nil {
		log.Fatalf("Error parsing template file: %v", err)
	}

	serviceArgs := struct {
		AppName      string
		AppNamespace string
		Team         string
		Ports        []models.Port
		Selectors    map[string]string
		ServiceType  string
	}{
		AppName:      config.Name,
		AppNamespace: namespace,
		Team:         config.Team,
		Ports:        config.Ingress.Ports,
		Selectors:    config.Ingress.Selector,
		ServiceType:  config.Ingress.Type,
	}

	// Execute the template with the data
	err = tmpl.Execute(file, serviceArgs)
	if err != nil {
		log.Fatalf("Error executing template: %v", err)
	}

}
func createAppYaml(config models.Config, path string) {

	appFileName := config.Name + ".yaml"

	file, err := os.Create(path + appFileName)
	if err != nil {
		log.Fatalf("Error creating file: %v", err)
	}
	defer file.Close()

	if config.AppOverride != nil {
		// Write the app override to the file
		_, err = file.WriteString(*config.AppOverride)
		if err != nil {
			log.Fatalf("Error writing app override to file: %v", err)
		}
		return
	}

	// Parse the template
	tmpl, err := template.New("app").Parse(appTemplate)
	if err != nil {
		log.Fatalf("Error parsing template file: %v", err)
	}

	// Define the data to pass to the template
	appArgs := struct {
		AppName string
		Team    string
	}{
		AppName: config.Name,
		Team:    config.Team,
	}

	// Execute the template with the data
	err = tmpl.Execute(file, appArgs)
	if err != nil {
		log.Fatalf("Error executing template: %v", err)
	}
}
