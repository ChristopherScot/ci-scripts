# ci-scripts

**homelabctl moved to
[ChristopherScot/homelabctl](https://github.com/ChristopherScot/homelabctl).**

This repo is retired. Its source is gone and it publishes no new
releases, so nothing here can drift away from the real one.

## If you have an old homelabctl

Run it twice:

```sh
homelabctl update   # installs v1.18.4, the last release from here
homelabctl update   # installs from ChristopherScot/homelabctl
```

v1.18.4 exists only to do that: it is built with its update command
pointed at the new repo, so one run moves you across. It is published
for macOS and Linux, Intel and ARM, and `update` picks the build
matching the machine it is running on.

## Why the releases are still here

The published releases below are deliberately left in place. Services
scaffolded before the move download `homelabctl_linux_amd64.tar.gz`
from this repo in CI, and those downloads still work. Deleting the
releases would break them.

## Installing fresh

Get it from the new repo, picking the build for your machine:

```sh
# macOS, Apple Silicon
curl -sL https://github.com/ChristopherScot/homelabctl/releases/latest/download/homelabctl_darwin_arm64.tar.gz | tar xz

# macOS, Intel
curl -sL https://github.com/ChristopherScot/homelabctl/releases/latest/download/homelabctl_darwin_amd64.tar.gz | tar xz

# Linux, x86
curl -sL https://github.com/ChristopherScot/homelabctl/releases/latest/download/homelabctl_linux_amd64.tar.gz | tar xz

# Linux, ARM
curl -sL https://github.com/ChristopherScot/homelabctl/releases/latest/download/homelabctl_linux_arm64.tar.gz | tar xz
```
