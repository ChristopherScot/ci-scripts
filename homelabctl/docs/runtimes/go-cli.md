# go-cli

A command-line tool written in Go. Not deployed to the cluster —
compiled for macOS and Linux and published as a downloadable release.

```sh
homelabctl init mytool --runtime go-cli
```

`homelabctl` itself is a `go-cli`.

## What you get

```
mytool/
  main.go           your commands — this is where you work
  main_test.go      tests
  update.go         a self-update command, written for you
  completion.go     tab completion, written for you
  VERSION           what version this is, and its changelog
  go.mod            dependencies
```

Notice what is **not** there: no `config.yaml`, no `deploy/`, no
Dockerfile. A CLI is not deployed, so none of that applies. `homelabctl
render`, `regen`, `check` and `vault` have nothing to do here.

Already working:

- **`mytool version`** — prints the version, stamped in by CI.
- **`mytool update`** — downloads and installs the latest release. Your
  users never have to find a download page.
- **`mytool completion install`** — sets up tab completion in their shell.
- **A release pipeline** that cross-compiles for macOS (Intel and Apple
  Silicon) and Linux (x86 and ARM).

It uses [Cobra](https://github.com/spf13/cobra), which is the standard
way to structure a Go CLI.

## Working on it

```sh
cd mytool
go run . --help
go test ./...
```

## Adding a command

In `main.go`, write a function returning a `*cobra.Command` and register
it:

```go
func greetCmd() *cobra.Command {
	var loud bool
	cmd := &cobra.Command{
		Use:   "greet <name>",
		Short: "say hello",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			msg := "hello, " + args[0]
			if loud {
				msg = strings.ToUpper(msg)
			}
			fmt.Println(msg)
			return nil
		},
	}
	cmd.Flags().BoolVar(&loud, "loud", false, "shout it")
	return cmd
}
```

Then add it to the root command:

```go
root.AddCommand(updateCmd(), versionCmd(), greetCmd())
```

`mytool greet world --loud` now works, and so does its `--help`.

## Releasing

**Edit `VERSION` and push.** That is the whole process.

The file looks like this — the first line is the version, and the rest is
a changelog you write:

```
v0.2.0

v0.2.0 add the greet command
v0.1.0 Initial release
```

CI releases **only when `VERSION` changes** on `main`. Tests run on every
push; a push that does not touch `VERSION` builds and releases nothing.
That means a release is always deliberate.

Once it is published, your users get it with:

```sh
mytool update
```

### Two things that must stay in step

Neither is checked at build time, so both fail quietly if you break them:

1. **`VERSION` drives releases.** Bump it in the commit that should ship.
   A change without one ships nothing, and looks exactly like a commit
   that was not meant to ship.

2. **`update.go` downloads `mytool_<os>_<arch>.tar.gz`** from the latest
   GitHub release, and CI builds assets with exactly that name. If you
   rename one side, self-update breaks against a release that looks
   perfectly fine on GitHub. Change both together or neither.

A locally built binary reports its version as `dev` and refuses to
self-update, which is deliberate — it stops a development build from
overwriting itself with a release.

## The whole repo is yours

This is the big difference from a service. A CLI has no `config.yaml`, so
nothing regenerates anything: `init` scaffolded these files once, and
every one of them is yours to edit freely.

The trade-off is that a later improvement to homelabctl's conventions
will never reach your tool. `init` is a starting point, not a template
you stay attached to.

## Where the binary goes

Users download it and put it on their `PATH`:

```sh
curl -sL https://github.com/owner/mytool/releases/latest/download/mytool_darwin_arm64.tar.gz | tar xz
mv mytool ~/bin/
```

On macOS, a downloaded binary is quarantined and will not run until:

```sh
xattr -d com.apple.quarantine ~/bin/mytool
```

Worth putting both of those in your tool's own README.
