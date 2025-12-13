# yaffw Agent Guide

## Architecture Context
- **Backend**: Go (Golang) for ALL services (Control & Compute Planes).
- **Frontend**: HTML, HTMX, Tailwind CSS, Franken-UI.
- **Data**: Postgres (Data), Redis (State/Queue).
- **Note**: This stack overrides any .NET references in `architecture.md`.

881M    ./Watcher (2022)
974M    ./Haunt (2019)

## Build, Lint & Test
- **Go**: 
  - Build: `go build ./...`
  - Test: `go test ./...` (Single: `go test -run <TestName>`)
  - Lint: `golangci-lint run`
- **Frontend**:
  - Use standard Tailwind CLI or build scripts for CSS generation.

## Code Style & Conventions
- **Go**: Idiomatic Go (gofmt). Explicit error handling (`if err != nil`). 
- **Web**: Server-side rendering (Go `html/template`).
- **Interactivity**: Use HTMX (`hx-get`, `hx-target`) over custom JS.
- **UI**: Franken-UI components with Tailwind utility classes.
- **Config**: NO local files. Use Environment Variables (K8s style).
