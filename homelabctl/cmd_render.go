package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
)

// Abbreviated SHAs are the mistake this catches: they look like valid tags
// and fail only at pull time, as ImagePullBackOff with the app still
// showing Synced.
var shortSHA = regexp.MustCompile(`:[0-9a-f]{7,12}$`)

func runRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	out := fs.String("out", ".", "directory to write manifests into")
	appOut := fs.String("app-out", "", "also write the Argo Application here")
	repoURL := fs.String("repo-url", "https://github.com/ChristopherScot/homelab", "repo the Application syncs from")
	appPath := fs.String("app-path", "", "path within that repo (default: service name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("usage: homelabctl render <config.yaml> <image-ref>")
	}
	cfgPath, imageRef := fs.Arg(0), fs.Arg(1)

	if shortSHA.MatchString(imageRef) {
		return fmt.Errorf("image ref %q ends in an abbreviated SHA; registry tags are full 40-char SHAs or digests", imageRef)
	}

	c, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if c.Image.Repository == "" {
		return fmt.Errorf("image.repository is required to render")
	}

	dir := filepath.Join(*out, c.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, o := range render.All(c, imageRef) {
		p := filepath.Join(dir, o.Path)
		if err := os.WriteFile(p, []byte(o.Body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
		fmt.Println("wrote", p)
	}

	if *appOut != "" {
		p := *appPath
		if p == "" {
			p = c.Name
		}
		if err := os.MkdirAll(filepath.Dir(*appOut), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(*appOut, []byte(render.Application(c, *repoURL, p)), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", *appOut)
	}
	return nil
}
