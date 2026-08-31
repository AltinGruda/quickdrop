# Releasing QuickDrop

## The short version (CI)

1. Push to `main` — that deploys the marketing site to GitHub Pages
   (`https://AltinGruda.github.io/quickdrop/`).
2. Tag a release — push a `vX.Y.Z` tag and GitHub Actions builds both apps and
   publishes a GitHub Release:

   ```powershell
   git tag v1.0.0
   git push origin v1.0.0
   ```

   That runs `.github/workflows/release.yml`, which builds:
   - `QuickDrop-setup.exe` — Windows NSIS installer (WebView2 embedded)
   - `QuickDrop.dmg` — the macOS app
   - `QuickDrop.exe` — the raw Windows binary (also inside the installer)

   and attaches all three to the release page. The website's download buttons
   resolve to the newest release automatically.

## What the CI builds

- **Windows** (`windows-latest`): installs Go + NSIS, then runs
  `scripts/build-release.ps1` → `wails build -nsis -webview2 embed`. Unsigned,
  so users get the normal one-time **"More info → Run anyway"** prompt.
- **macOS** (`macos-latest`): installs Go, runs `wails build -platform darwin`,
  then wraps the `.app` in a DMG with `hdiutil`. Unsigned, so users must
  right-click → **Open** the first time.

No code signing is wired up yet (needs paid certs + notarization + repo secrets).

## Local builds (developer fallback)

Prerequisites: Go, Wails CLI
(`go install github.com/wailsapp/wails/v2/cmd/wails@latest`), and NSIS 3
(`build\windows\installer` is used by the Windows installer build).

### Windows

```powershell
.\scripts\build-release.ps1
```

Produces:
```
build\bin\QuickDrop Setup.exe    <- hand this to users
build\bin\QuickDrop.exe          <- the raw app
```

Optional signing (`-CertThumbprint`, `-PfxPath`/`-PfxPassword`) is supported by
`build-release.ps1` / `scripts/sign.ps1` but not required.

### macOS

```bash
wails build -platform darwin     # build/bin/QuickDrop.app
```

## Website

The `website/` folder is a fully static single page (self-hosted fonts, no build
step). `.github/workflows/pages.yml` publishes it to GitHub Pages on every push
to `main` that touches `website/`. It also needs the repo configured once:

> **Settings → Pages → Source: GitHub Actions**

## Checks before shipping

```powershell
go test ./...
node tests\sha256.test.mjs   # cross-checks the phone's SHA-256
```

Manually on real machines:

1. Installer installs cleanly on a PC with no WebView2.
2. App shows the QR code in under ~2 seconds on first launch.
3. Scanning with a phone camera opens the page; a photo uploads and lands in
   `Downloads\QuickDrop` with an accurate progress bar.
4. Phone locks mid-transfer; the file still finishes when the phone is unlocked.
5. Two phones upload two files with the same name; both appear (second gets ` (1)`).
6. On a phone that is on the Wi-Fi but blocked from reaching the PC, the PC shows
   the "can't reach this computer" warning within a few seconds.

## Developer notes (quirks on this machine)

- **Application Control blocks `wails generate module`** on Windows; don't use
  that subcommand. `wails build` regenerates bindings anyway.
- **`makensis` is not on PATH** even when NSIS is installed; `build-release.ps1`
  adds the NSIS directory to PATH itself.
- **Go: `//go:embed` only works with directory patterns on this toolchain**.
  Keep embedded assets behind a directory pattern and read with `fs.ReadFile`.

## Layout

- `server\` — the HTTP/session server, upload protocol, LAN-IP discovery, debug log
- `server\frontend\` — the phone-facing page (`upload.html`/`.js`/`.css`), plain JS
- `gui\index.html` — the computer-side window (Wails)
- `website\` — the public marketing/landing page deployed to GitHub Pages
- `app.go`, `main.go` — Wails app wiring and bindings
- `cmd\quickdrop-cli\` — headless dev launcher
- `tests\sha256.test.mjs` — phone-side SHA-256 verification
- `.github\workflows\` — CI (release builds + Pages deploy)
- `scripts\` — build + sign tooling
