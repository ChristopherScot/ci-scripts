# go-tui

A terminal application written in Go — a full-screen interface you
navigate with the keyboard, rather than a command that prints and exits.
Think `htop` or `lazygit`.

```sh
homelabctl init mytui --runtime go-tui
```

Not deployed to the cluster. Like a [`go-cli`](go-cli.md), it is
cross-compiled and published as a downloadable release.

## go-cli or go-tui?

| | `go-cli` | `go-tui` |
|---|---|---|
| you run it and | it prints something and exits | it takes over the terminal until you quit |
| good for | scripts, one-off commands, automation | browsing, monitoring, anything interactive |
| example | `git commit` | `lazygit` |

If you are not sure, pick `go-cli`. A TUI is more work to write and only
pays off when someone is going to sit in it.

Everything in [go-cli.md](go-cli.md) about releasing, self-update and
`VERSION` applies here identically — the two share a release pipeline.

## What you get

```
mytui/
  main.go           starts the program
  model.go          the interface — this is where you work
  main_test.go      tests
  update.go         a self-update command, written for you
  completion.go     tab completion, written for you
  VERSION           what version this is, and its changelog
```

A **working, runnable interface** out of the box: a scrollable list with
filtering, help text and keyboard navigation. Run it and see.

```sh
cd mytui
go run .
```

`/` filters the list, arrow keys move, `q` quits.

Also included, the same as a `go-cli`:

- `mytui version`, `mytui update`, `mytui completion install`
- a release pipeline for macOS and Linux

Subcommands still work — `mytui version` prints the version rather than
launching the interface. Only running `mytui` with no arguments starts
the UI.

## How a TUI is structured

It uses [Bubble Tea](https://github.com/charmbracelet/bubbletea), which
follows a pattern borrowed from the Elm language. Three pieces, all in
`model.go`:

| piece | what it does |
|---|---|
| **the model** | one struct holding all your state |
| **`Update`** | receives a message, returns the *next* state |
| **`View`** | draws the current state, and nothing else |

Everything that happens — a key press, a window resize, a background task
finishing — arrives at `Update` as a message. `Update` does not change
the current state; it returns a new one. `View` only draws, with no side
effects of any kind.

**Nothing slow goes in `Update`.** It runs on the event loop, so blocking
there freezes the interface. Slow work goes in a `tea.Cmd`, which runs
elsewhere and reports back as another message.

```go
case tea.KeyPressMsg:
    if msg.String() == "q" {
        return m, tea.Quit
    }
```

## ⚠️ This is Bubble Tea v2

Most examples and tutorials online are for v1, **and they will not
compile here.** The API changed:

| v1 | v2 |
|---|---|
| `Init()` returns `(Model, Cmd)` | returns only `Cmd` |
| `View()` returns `string` | returns `tea.View` |
| `tea.KeyMsg` | `tea.KeyPressMsg` |
| alt-screen is a program option | a field on the View |

If you copy code from a blog post and it does not build, this is almost
certainly why. The upgrade guide is `UPGRADE_GUIDE_V2.md` inside the
bubbletea module.

The scaffold also brings in
[bubbles](https://github.com/charmbracelet/bubbles) for ready-made
components (lists, text inputs, spinners) and
[lipgloss](https://github.com/charmbracelet/lipgloss) for styling.

## Adding to the interface

Start in `model.go`. Add a field to the `model` struct for whatever state
you need, handle the relevant message in `Update`, and draw it in `View`.

The scaffolded list is a real, working example of all three — read it
before writing your own.

## The whole repo is yours

Same as `go-cli`: no `config.yaml`, so nothing regenerates. Every file is
yours to edit, and a later change to homelabctl's conventions will not
reach your tool.

## Releasing

Identical to `go-cli`: edit `VERSION`, push, and CI publishes. Users
update with `mytui update`. See [go-cli.md](go-cli.md#releasing) for the
details, including the two couplings that fail quietly if broken.
