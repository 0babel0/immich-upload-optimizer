# CLAUDE.md

Guidance for working in this repository.

## What this is

`immich-upload-optimizer` (IUO) — fork `joojoooo` of the original `miguelangel-nubla` project.
A small Go **reverse proxy placed in front of an Immich server**. It intercepts the
Immich HTTP API and transparently transcodes media using **external CLI tools**
(`cjxl`/`djxl`, `avifenc`/`avifdec`, `ffmpeg`, `magick`, `exiftool`) to save storage,
while keeping the Immich mobile/web apps unaware that a different file was stored.

Module: `github.com/joojoooo/immich-upload-optimizer` — Go 1.26. No database; state is a
CSV checksum map on disk.

## How it works (request flow)

All traffic goes through `handleRequest` in [main.go](main.go). Anything not handled by IUO
is forwarded verbatim to the upstream Immich server (`proxy.ServeHTTP`).

- **Upload** (`POST /api/assets`, multipart) → `newJob` in [jobs.go](jobs.go).
  Streams the multipart body, runs a matching task, uploads the *processed* file if it is
  smaller than the original (otherwise uploads the original). Deduplicates concurrent
  uploads by `deviceAssetId|filename`.
- **Task processing** → `TaskProcessor` in [tasks.go](tasks.go). Picks the first task whose
  `extensions` match, renders its command template, runs it via `sh -c`, and expects exactly
  one output file in the temp work dir. **The stored filename's extension is swapped**
  (`photo.webp` → `photo.jxl`) at [tasks.go](tasks.go) (`ProcessedFilename`).
- **Checksum/name spoofing** → [checksum.go](checksum.go). Because IUO stores a different
  file than the client uploaded, it rewrites checksums and filenames in Immich's
  sync/bulk-upload-check responses so the apps don't see duplicates or try to re-upload.
  The original↔stored checksum map is persisted to `checksums.csv`.
- **Download conversion** (`GET /api/assets/{uuid}/original`) → `downloadAndConvertImage`
  in [main.go](main.go). When `IUO_DOWNLOAD_JPG_FROM_JXL` / `_AVIF` is set, JXL/AVIF are
  converted to JPG **on the fly for the response only** — the stored asset is never modified.
- **WebSocket** (Immich real-time sync) is proxied through [websocket.go](websocket.go).

## Config / env

Flags mirror env vars with the `IUO_` prefix (via viper), e.g. `IUO_UPSTREAM`, `IUO_LISTEN`
(default `:2284`), `IUO_TASKS_FILE`, `IUO_DOWNLOAD_JPG_FROM_JXL`, `IUO_DOWNLOAD_JPG_FROM_AVIF`,
`IUO_MAX_IMAGE_JOBS`, `IUO_MAX_VIDEO_JOBS`. `TMPDIR` should point at a tmpfs so temp files
live in RAM (protects disk lifespan) — set by the Docker compose example.

Task files are YAML (`config/*.yaml`, and the user's runtime `client-config/tasks.yaml`,
which is gitignored). Each task = `name`, `command` (Go `text/template` with `{{.folder}}`,
`{{.name}}`, `{{.extension}}`, `{{.result_folder}}`, `{{.original_name}}`), `extensions`,
optional `min_filesize`. The command must produce exactly **one** output file.

## JXL ⇄ JPG: the important gotcha

`djxl file.jxl out.jpg` attempts **bit-exact JPEG reconstruction**, which only works if the
JXL carries JPEG bitstream reconstruction data (**jbrd**). jbrd exists **only** when the JXL
was made by `cjxl --lossless_jpeg=1` **from a real JPEG**.

JXL transcoded from **WEBP/PNG/HEIC** (e.g. `cjxl --distance=0`, as in the user's
`client-config/tasks.yaml`) has **no jbrd**. On such files `djxl` cannot reconstruct a JPEG
and (depending on the build) either silently falls back to a lossy pixel re-encode or emits an
unusable file — while still exiting 0. Detection: the reconstruction path prints
`Reconstructed to JPEG.`; the fallback prints `could not decode losslessly … Decoded to pixels.`.
The download path handles this by re-encoding pixels explicitly (`djxl --pixels_to_jpeg -q 95`)
and validating the JPEG SOI marker (`FF D8`) before serving; on failure it returns an error so
the original asset is proxied untouched.

## Build / run

**No Go toolchain is assumed on the dev host** — verify before running `go` commands.
Production is Docker (`Dockerfile.goreleaser`, built via goreleaser) which compiles the image
CLIs (libjxl/libavif/libheif/ImageMagick) from source. Release vars `version`/`commit`/`date`
in [main.go](main.go) are injected by goreleaser; when `version == "dev"` the proxy routes
through a local MITM proxy (`localhost:8080`) for development.

- Build: `go build ./...`
- Format/vet before committing: `gofmt -l .` and `go vet ./...`
- Run locally (example): `go run . -upstream http://immich-server:2283 -tasks_file config/lossy_avif.yaml`

## Conventions

- Logging: use the color helpers (`red`/`yellow`/`green`/`cyan`…) and the `customLogger`
  (`logger.Print`, `logger.Printf`, `logger.Error`). `logger.Error(err, "ctx")` logs and
  returns `true` when `err != nil` — the codebase uses it inline as `if logger.Error(...) { return }`.
- Errors returned from `downloadAndConvertImage`/handlers cause fall-through to the upstream
  proxy, so **never write to the ResponseWriter before you're sure the conversion succeeded**.
- Keep changes minimal and match the surrounding style; comments explain constraints, not steps.
