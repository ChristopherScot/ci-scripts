// homelabctl scaffolds services and renders their Kubernetes manifests.
//
//	homelabctl init <name> --runtime go [--monorepo] [--host H]
//	homelabctl render <config.yaml> <image-ref> [--out DIR] [--app-out FILE]
//	homelabctl check <dir>
//
// One tool so there is one config schema and one place that encodes the
// cluster's conventions. Languages are plugins (internal/runtime); adding
// Node or Python does not touch the manifest, Argo or CI logic.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = runInit(os.Args[2:])
	case "render":
		err = runRender(os.Args[2:])
	case "check":
		err = runCheck(os.Args[2:])
	case "update":
		err = runUpdate(os.Args[2:])
	case "version":
		fmt.Println(Version)
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `homelabctl - scaffold services and render their manifests

  init <name> --runtime go [--monorepo] [--host HOST] [--team TEAM]
      Create a new service, as its own repo or as services/<name>/.

  render <config.yaml> <image-ref> [--out DIR] [--app-out FILE]
      Render manifests. Used by CI and to regenerate after a convention
      change. image-ref must be a full SHA or digest - an abbreviated SHA
      is not a registry tag and yields ImagePullBackOff.

  check <dir>
      Fail on the deploy misconfigurations that are otherwise silent.

  update [--check]
      Update this binary to the latest release.

  version
      Print the version.
`)
}
