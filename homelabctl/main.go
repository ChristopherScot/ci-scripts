// homelabctl scaffolds services and renders their Kubernetes manifests.
//
// One tool so there is one config schema and one place that encodes the
// cluster's conventions. Languages are plugins (internal/runtime); adding a
// runtime does not touch the manifest, Argo or CI logic.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// defaultConfigPath is what commands use when no config file is named, so
// you never type it while standing in the service directory.
const defaultConfigPath = "config.yaml"

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "homelabctl",
		Short: "scaffold services and render their manifests",
		// Usage on every error buries the error itself; cobra still prints
		// usage for genuine usage mistakes.
		SilenceUsage: true,
		// Cobra prints the error itself; main prints it too, so without
		// this every failure appears twice.
		SilenceErrors: true,
	}
	root.AddCommand(initCmd(), renderCmd(), regenCmd(), diffCmd(), checkCmd(), vaultCmd(), updateCmd(), versionCmd())

	// Cobra builds its own `completion` command during Execute, so it does
	// not exist yet here and cannot be extended in place. Force it to be
	// created now, then hang `install` off it.
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			c.AddCommand(completionInstallCmd("homelabctl"))
		}
	}
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the version",
		Args:  cobra.NoArgs,
		Run:   func(_ *cobra.Command, _ []string) { fmt.Println(Version) },
	}
}

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
