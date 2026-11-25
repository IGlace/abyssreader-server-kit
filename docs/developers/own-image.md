<!--
SPDX-License-Identifier: CC-BY-NC-SA-4.0
Copyright 2026 Abyss Reader
-->
# Fork and use your own image

Read [LICENSING.md](../../LICENSING.md) before copying or changing the kit.
Keep the notices and run the [build and tests](README.md) in your fork.

Build the image with a fresh tag. Use that exact tag in a private `.env`
answers file on the install host and fill out the remaining settings from
the [install guide](../install.md#scripted-installs-the-answers-file).

```sh
docker build --build-arg VERSION=1.0.0-local -t my-kit:1.0.0-local .
sudo sh install.sh --answers .env
```

Add `KIT_IMAGE=my-kit:1.0.0-local` to the answers file before installing.
Use a tag you control or a pinned digest. The installer picks the answer
on a fresh install and records the exact reference in Compose and `.env`.
Docker uses a local image when the exact reference is already present.
The installer identifies the custom image and its support limits. Confirm the
image when prompted in an interactive terminal.

To upgrade, merge upstream changes and rebuild under a new tag. Back up
and export a copy first. Check migration compatibility and test the rebuilt
image with a throwaway volume before using existing data. Then have the
operator update both the Compose image and `KIT_IMAGE` in the installed
`.env` to the same reference and recreate the service. Retain the previous
image and matching backup for recovery. The installer release upgrade
command tracks official images. Verify the running kit with the app's
conformance check. Kit support covers official release images only.
