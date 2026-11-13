# Agent and contributor notes

For local installation and use, follow [USAGE.md](USAGE.md) from top to bottom. It identifies steps that require a human, protects credentials, and describes how to verify setup.

For repository work, read the relevant Go code before documenting or changing behavior. Build with `make build`; verify changes with `go vet ./...` and `go test ./...`. `make package` builds the release targets defined by `scripts/build-release.sh`. Keep generated binaries and local mail data out of commits. Do not send email as part of tests or documentation verification.
