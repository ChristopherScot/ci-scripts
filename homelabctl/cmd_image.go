package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// imageCmd prints the registry path this service publishes to.
//
// It exists so the CI workflow can ASK for the image name instead of
// remembering it. The name used to be written twice: into config.yaml,
// which reaches the manifests through every `render`, and into the
// workflow's `images:` line, which `init` wrote once and never
// revisited. Only the first followed a rename, so CI published under one
// name while the cluster pulled another - and nothing said so. The build
// was green, because it pushed an image successfully. `render` was
// clean, because the manifests matched the config. Argo reported Synced,
// because it applied what it was given. The only symptom was
// ImagePullBackOff, and on a first deploy there is no previous image to
// fall back to, so the service simply never started.
//
// One line of output and nothing else, because the caller is a shell:
//
//   - id: image
//     run: echo "ref=$(./homelabctl image)" >> "$GITHUB_OUTPUT"
//
// No tag. The workflow's metadata-action owns tagging, and this answers
// only "what is this image called".
//
// This does NOT couple the deploy plane to the build plane. The fact
// lives in config.yaml, which both already read; render is untouched,
// and internal/render and internal/runtime still do not import each
// other. What changes is that the workflow holds a question rather than
// a copy of the answer.
func imageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "image",
		Short: "print the image repository this service publishes to",
		Long: "Print image.repository from config.yaml, so CI reads the same\n" +
			"field the manifests do rather than a copy frozen at scaffold time.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := findConfig()
			if err != nil {
				return err
			}
			return runImage(cmd.OutOrStdout(), path)
		},
	}
}

func runImage(w io.Writer, cfgPath string) error {
	c, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	// Refused rather than printed empty. An empty images: input does not
	// fail docker/metadata-action - it produces no tags, and
	// build-push-action then pushes nothing while the job stays green.
	// That is the same silent failure one step over, so it dies here.
	if c.Image.Repository == "" {
		return fmt.Errorf("image.repository is not set in %s, so there is no image to publish", cfgPath)
	}
	fmt.Fprintln(w, c.Image.Repository)
	return nil
}
