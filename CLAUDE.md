# CLAUDE.md

Guidance for Claude Code when working in this repo. Organised by topic, not by date — the
hard-won debugging findings are in "Why things are the way they are", and they are the
most valuable part of this file. Read that section before changing networking or calls.

## What this is

**Cloudix** — a serverless P2P messenger (Wails v2 desktop app) for macOS and Windows.
Text, media, reactions, read receipts, typing indicator, audio/video calls, screen sharing,
block list, local "Saved" notes, RU/EN, three themes. No accounts, no company server: the
profile and history live in a local SQLite file.

Three ways two people can reach each other:

1. **LAN** — UDP multicast discovery, direct TCP. Zero configuration.
2. **Cloudix network, direct** — one peer hosts on a public IP + forwarded port; others
   join with a name, a password and an invite code.
3. **Cloudix network, via relay** — both peers connect *out* to a relay the user runs
   themselves. This is what makes it work behind CGNAT.

## Current state (2026-09-13)

| Branch | Commit | Contents |
|---|---|---|
| `main` | `2ac6e18` | Pure P2P: LAN + direct-hosted overlay. No relay. |
| `dev` | `2ac6e18` | Same as main. |
| `relay-server` | `d091e97` | **Active branch.** Adds the optional relay server. |

Working and confirmed by the user: messages, media, reactions, screen sharing, and
**calls across different networks** (which turned out not to need TURN in their case).
The relay is deployed on their VPS and carries messages.

TURN is implemented and configurable but the user has not needed it yet; coturn setup is
documented in the README for the case where both peers are behind CGNAT.

## Build / run

```bash
wails dev                                # hot reload
wails build -platform darwin/universal   # -> build/bin/cloudix.app  (lowercase!)
wails build -platform windows/amd64      # -> build/bin/cloudix.exe

# relay server for a VPS
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
  -o build/bin/cloudix-relay-linux-amd64 ./cmd/cloudix-relay

go build ./...              # needs frontend/dist to exist (go:embed)
cd frontend && npm run build
go test ./backend/vpn/      # the only tests; they run the real relay binary
go vet ./...                # clean, keep it that way
```

### ⚠️ Do not launch the app for the user from Bash

Windows started from the agent's Bash tool **inherit its restricted network**: UDP replies
never come back, so no srflx ICE candidates are gathered. This made a working Mac look
broken and cost several rounds of misdiagnosis. Build for the user, but let them run it:

```
open build/bin/cloudix.app
```

Launching two instances to smoke-test that the app *starts* is fine; drawing conclusions
about its networking from them is not.

### Two local instances

`CLOUDIX_INSTANCE=<x>` gives a separate DB dir (`~/Library/Application Support/Cloudix-<x>/`).

```bash
BIN=build/bin/cloudix.app/Contents/MacOS/cloudix
CLOUDIX_INSTANCE=a "$BIN" & CLOUDIX_INSTANCE=b "$BIN" &
```

Messages work this way. **Calls between two instances on one machine do not** — both
resolve to the same IP and the loopback path is unreliable. Real call testing needs two
machines.

## Layout

```
main.go                      Wails bootstrap; Frameless on Windows only
backend/app/app.go           All bound methods + handleEnvelope router (~1500 lines)
backend/transport/           TCP: dial/accept, conn pool by peerId, 96 MiB frame cap
backend/discovery/           UDP multicast + unicast announce/listen, peer TTL
backend/models/models.go     Wire types, envelope + signal kind constants
backend/storage/storage.go   SQLite. Migrations = CREATE IF NOT EXISTS + best-effort ALTER
backend/vpn/                 Overlay network: crypto, host, client, relay transport, invites
cmd/cloudix-relay/           Standalone relay server for a VPS
frontend/src/App.jsx         Entire UI (~4700 lines, single file)
frontend/src/i18n.js         RU/EN dictionary — complete, keep it that way
frontend/src/styles/theme.css All CSS (~2700 lines), token-driven
frontend/wailsjs/            Generated bindings, committed; `wails build` regenerates
```

## Stack

- **Go 1.25**, Wails v2.13, SQLite via `modernc.org/sqlite` (pure Go, no cgo).
  `golang.org/x/crypto` (argon2, chacha20poly1305, curve25519, hkdf, blake2b) and
  `golang.org/x/sys` are direct dependencies.
- **React 18 + Vite 5 + Framer Motion.** One component file.
- **Transport:** newline-delimited JSON over TCP. Oversized frames are skipped, not fatal.
- **Discovery:** UDP multicast `239.255.42.99:47990` + unicast fallback on the same port.
- **Calls:** WebRTC. Signalling rides the normal envelope channel. STUN by default,
  **TURN optional and user-configured** (`cloudix:ice`).

## Conventions

- Bound methods are PascalCase on `*App`, re-check `a.store != nil` / `a.getProfile()`,
  and return a plain `fmt.Errorf`.
- **`a.deliver(peerID, env)` is the single outbound path** for every envelope type: LAN
  transport first, overlay as fallback, overlay-only peers straight through. Do not call
  `transport.Send` directly in new code.
- Sends that can block go in a goroutine and report failure via `a.emitEvent`, never by
  blocking the bound call.
- After changing a bound method or a `models.*` struct, regenerate bindings (`wails build`)
  and keep `frontend/wailsjs/go/` committed in sync.
- New user-facing strings go through `t.*` in **both** `ru` and `en`.
  **Never store a localized string in the DB** — media previews are neutral tokens
  (`models.PreviewImage/Video/File`) rendered by `previewText(raw, t)`.
- CSS only in `theme.css`, using the custom properties (`--panel`, `--accent`, `--ease`…).
  A literal colour will not follow the themes.
- Any new fixed-width UI needs an entry in the responsive breakpoints (900 / 760 / 640 px
  wide, 560 px tall) or it overflows the 620×460 minimum window.
- `FIX:` / `NEW:` comments document past bugs — leave them.

### Events (Go → JS)

`peers:update` · `message:incoming|deleted|read|reacted|delivered` · `ping:result` ·
`profile:updated` · `account:deleted` · `signal:incoming` · `signal:send_error` ·
`vpn:status`

### localStorage keys

`cloudix:theme` · `cloudix:lang` · `cloudix:relay` · `cloudix:ice` · `cloudix:mic-device` ·
`cloudix:screen-quality` · `cloudix:screen-width` · `cloudix:saved-messages:<peerId>`

Settings that must apply mid-call dispatch a window event rather than waiting for a
restart: `cloudix:screen-quality-changed`, `cloudix:mic-changed`.

## The overlay network (`backend/vpn`)

**Not a system VPN.** A real one needs a virtual adapter — signed kernel driver on Windows,
Network Extension entitlement on macOS, admin rights on both. This carries Cloudix's own
traffic only; games and other apps are not routed. Say so plainly when asked.

**Shape.** One peer is the `Host`, others are `Client`s; the host relays between members.
In direct mode the host listens on TCP 47991 (NAT-PMP port mapping attempted, manual
forward documented). In relay mode both sides dial out to `cloudix-relay`.

**Crypto.**
- `DeriveNetworkKey` = Argon2id(password, salt = blake2b(normalised name)), 64 MiB, t=3.
- Only `NetworkID` — a blinded hash of that key — goes on the wire.
- **Authentication is implicit.** The network key is folded into the HKDF salt behind every
  link key, so a wrong password derives a different key and the first sealed frame simply
  fails to open. There is no check to bypass.
- XChaCha20-Poly1305 with random 24-byte nonces over X25519 ECDH.
- `E2EKey` sorts the two public keys so both sides agree; **the host cannot read what it
  relays** because it holds neither private key.
- Identity keys are pinned TOFU in `vpn_pins`. A changed key is refused and surfaced — that
  is what interception looks like. `Fingerprint()` is shown in the UI for out-of-band
  comparison.

**Invite codes carry the network name and host address but never the password.** Keep it
that way: a leaked code alone must grant nothing.

**Session memory.** `Service` records how the session was entered (`sessionParams`);
`Reconnect()` rebuilds it after a network change, and a client that drops retries with
backoff. `Leave()`/`Dropped()` clear that memory so a pending retry cannot resurrect a
session the user ended.

## The relay (`cmd/cloudix-relay`)

Deliberately dumb and untrusted. It pairs two TCP connections naming the same room and
copies bytes. It is addressed by the blinded network id, takes no part in authentication,
holds no key, and forwards already-sealed bytes.

- **The relay is user-supplied, never hardcoded.** Address + token live in `cloudix:relay`
  and are typed into the network panel. Do not add a default server.
- `-token` guards the server's bandwidth, **not** confidentiality. The UI says so; keep it.
- Prefer `CLOUDIX_RELAY_TOKEN` over the flag: a flag is visible in `ps` and `systemctl cat`.
- Hosts ping every 15s; the relay releases a room after 45s of silence, and
  `RelayListener.Close()` sends an explicit `bye` so leaving frees the room immediately.
- `RelayListener` implements `net.Listener`, so `Host.acceptLoop` is unchanged;
  `Client.handshake` is split out of `Connect` so it runs identically either way.

## Why things are the way they are

Each of these cost real debugging. Do not "simplify" them away.

**ICE candidates are mDNS names.** WebViews publish host candidates as `<uuid>.local`;
macOS WebKit and Windows WebView2 cannot resolve each other's. `app.peerIP()` finds the
peer's real address (live TCP connection first, then discovery) and ships it as `peerIp` on
every `signal:incoming`; `rewriteMdnsCandidate` and `rewriteSdpMdns` substitute it in
trickled candidates *and* inside offer/answer SDP. `addRemoteCandidate` then tries the
rewritten form, the original, and a BUNDLE-normalised form, counting what was accepted.

**Signals arriving before `CallModal` mounts were dropped.** An incoming offer creates
`callState`; the modal only registers its handler on mount, and the first burst of ICE
lands in that gap. This is why roughly one call in ten used to connect. `pendingSignalsRef`
buffers and replays to the first handler that registers (the offer is excluded — it travels
as `incomingOffer`).

**TCP connection glare.** Both peers dial each other during call setup. `readLoop` used to
close any existing connection when registering an inbound one, so crossed dials made each
side close the socket the other was about to write on, killing the link in one direction.
Registration now leaves the existing connection open: reads are served by every connection,
writes go to the most recent.

**`setParameters` does not survive renegotiation.** The screen-share bitrate cap was
applied once at `addTrack` and silently lost, dropping the encoder to its default ceiling —
that was the blocky picture. `applyScreenEncoding()` re-runs after every answer.

**`ontrack` does not fire again when a transceiver is reused.** Stopping and restarting a
share left the receiver bound to a stale muted track — the black square. The share is
anchored to the **transceiver mid** (stable across renegotiation, unlike track ids);
`announceScreen()` re-sends `screen-on` after each negotiation and `resolveScreenTracks()`
sweeps `pc.getReceivers()` rather than trusting the event.

**Chat rows must be created explicitly over the overlay.** `discovery` only knows LAN
peers, so an incoming overlay message found no chat row, `TouchChatLastMessage` updated
nothing, and the conversation stayed invisible. `ensureChatMeta` falls back to the overlay
roster and then to a placeholder row.

**A malformed ICE server URL throws in the `RTCPeerConnection` constructor**, which took
down every call rather than just TURN. `normalizeTurnUrl` adds a missing scheme and a
rejected config falls back to STUN with a message naming the setting.

**`discovery.Restart()` raced.** Loop goroutines now take their stop channel by value and
Start/Stop/Restart are serialized by `runMu`, so a restart retires the old generation.

**The unicast listener could not bind.** It shares :47990 with the multicast socket, which
needs SO_REUSEADDR + SO_REUSEPORT via `net.ListenConfig{Control: reuseControl}`
(`reuse_unix.go` / `reuse_windows.go`).

## Platform limits (not bugs — do not try to fix in code)

- **macOS cannot share system audio.** WebKit's `getDisplayMedia` returns no audio track.
  Workaround wired into Settings: install a loopback driver (BlackHole), route output into
  it, select it as the share audio source; it is captured with `getUserMedia`.
- **`getDisplayMedia` ignores resolution constraints on macOS.** Resolution is enforced
  encoder-side via `scaleResolutionDownBy` computed from `track.getSettings().height`.
- **WKWebView ignores the HTML `download` attribute for data: URLs.** Media saving goes
  through the bound `SaveMedia(name, dataURL)` and a native dialog on both platforms.
- **`requestFullscreen` is a no-op in WKWebView** and blacked out the whole window in
  WebView2. The screen viewer's "expand" fills the app window instead.
- **CGNAT cannot be worked around by router configuration.** If the WAN IP is private
  (`10.x`, `100.64–100.127`, `192.168.x`) while the public IP differs, hosting is
  impossible — the answer is a relay, a public IP from the ISP, or the other peer hosting.

## Window chrome

`<html data-platform>` is guessed from the UA before first paint, then confirmed via
`Environment()`. macOS keeps the native traffic lights (`TitleBarHiddenInset`) and rounded
shell. **Windows runs frameless** and renders `WindowsTitlebar`. `AppTitlebar` picks the
right one and **must be used on every root screen** (app, onboarding, disclaimer) — a
frameless window with no bar cannot be closed. Corners stay square on Windows: the window
is opaque, so CSS rounding shows black. Gate all chrome CSS on `[data-platform=…]`.

## Still open

- **No sender authentication on the LAN path.** `env.SenderID` is an unverified string, so
  anyone on the same network can spoof messages, deletes or `account_deleted`. Documented
  as the threat model (trusted network only) in README and the in-app docs panel. The
  overlay path *is* authenticated — payloads are sealed with a key only the two peers
  derive. A LAN fix means per-peer key pinning plus envelope signatures.
- **No message pagination.** A whole chat, media inline as base64, loads into memory on
  open.
- `models.SignalPayload.Name/Username` exist but are never populated. Harmless dead fields.
- Lockless access to `a.store` / `a.transport` / `a.discovery` versus `Logout()`'s
  nil-assignment. New code snapshots into locals to match; a real fix needs an RWMutex.
- The crypto has had no external audit. Primitives are standard; the assembly is not
  reviewed. Say this plainly rather than implying more assurance than exists.

## Working with this user

- They test on a real Mac ↔ Windows pair and report precise symptoms — take the reports
  seriously, they have been right about every bug so far.
- The call diagnostics panel (in the call card) is the fastest path to a root cause: it
  reports ICE states, candidate counts by type, sent/received/added/rejected, and the
  selected pair. Ask for it from **both** sides before theorising.
- They asked for CLAUDE.md to be kept current without being asked each time.
- Deliverables they expect at the end of a pass: rebuild `build/bin` (macOS app, Windows
  exe, both relay binaries + `SHA256SUMS`), commit, push, update README if behaviour
  changed.
- Answer in Russian.
