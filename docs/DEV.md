# Harness dev guide

Prereqs: Go (1.26 in go.mod, installed toolchain), Node 24, and the Wails v3 CLI:

    go install github.com/wailsapp/wails/v3/cmd/wails3@latest
    export PATH="$PATH:$HOME/go/bin"
    wails3 doctor

## Run (dev, hot reload)

    wails3 dev            # builds Go, starts Vite on :9245, launches bin/harness.dev.app

Open the Monaco worker smoke test by loading the frontend with `?monacotest`
(e.g. http://127.0.0.1:9245/?monacotest in a browser, or temporarily change the
window `URL` in main.go to "/?monacotest").

## Build

    wails3 build         # -> bin/harness  (runs go mod tidy, bindings, npm install, vite build)
    ./bin/harness

Frontend only:

    cd frontend && npm install && npm run build     # tsc --noEmit + vite build -> frontend/dist

`frontend/dist/.gitkeep` is kept (the Vite config re-creates it) so `//go:embed all:frontend/dist`
compiles on a fresh checkout before the frontend has been built.

## Test

    go build ./... && go vet ./... && go test ./...
    go test -race ./internal/stream

## Bindings

Go services in `internal/app` registered in main.go are exposed to TS. Regenerate with:

    wails3 generate bindings -clean=true -ts -i -d frontend/src/lib/bindings

(`wails3 build` / `wails3 dev` do this automatically; the Taskfile passes `-d frontend/src/lib/bindings`.)
Output is generated and the directory is wiped on each run, so put nothing else there.
Import style:

    import { BusInfo } from "./bindings/github.com/sid/harness/internal/app/busservice";

## Bus debugging

    HARNESS_BUS_TAP=1 wails3 dev      # frames -> ~/Library/Application Support/Harness/logs/bus-<ts>.ndjson

## Notes

- Agent host mode: `./bin/harness --agent-host <run>` (stub, returns "not implemented").
- Linux, Windows, iOS, Android template scaffolding was removed (macOS only). `build/windows/` and
  `build/build/` are scratch dirs created by the icon task and are git-ignored.
- ld "built for newer macOS version" warnings during Go link are harmless.
- Deps for later work are pinned in `internal/deps.go` (`//go:build tools`), so go.mod needs no edits.
