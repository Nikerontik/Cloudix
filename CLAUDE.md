# CLAUDE.md

Guidance for Claude Code when working in this repo.

## What this is

**Cloudix** — a serverless P2P messenger (Wails v2 desktop app). Works over LAN / Wi-Fi
or a VPN tunnel (RadminVPN, Hamachi). No central server, no accounts — a local profile
stored in SQLite. Currently a working first version: text messages, media, reactions,
read receipts, typing indicator, audio/video calls (WebRTC), block list, local "Saved"
notes, RU/EN i18n, light/dark themes.

## Session state (2026-09-04)

**Mac ↔ Windows calls still fail.** First real diagnostics (both sides, `ice-conn: checking`,
`pair: none succeeded yet`):

| | Mac | Windows |
|---|---|---|
| local candidates | `host:4` (no srflx — STUN unreachable) | `host:8 srflx:1` |
| remote candidates | `srflx:1` | `host:1` |

Both hosts are on the same /24 (`192.168.10.248` / `.132`). The signalling channel is
reliable TCP and messaging works, so **candidates are being lost after delivery** — almost
certainly rejected by `addIceCandidate` (the old code swallowed those errors). Mac accepted
none of Windows' 8 host candidates; those are exactly the mDNS-rewritten ones.

This pass: `addRemoteCandidate` now tries the rewritten form, the original form, and a
BUNDLE-normalised form (`sdpMid` forced to the first mid), counting sent / received /
added / rejected plus the last rejection error and candidate — all shown in the call
diagnostics panel. **Next test must report `cand sent/recv/added/rejected` and
`reject err` from both sides.** Also added two more STUN servers (the Mac got no srflx).

Not yet ruled out: Windows Firewall dropping inbound UDP for the app, WebKit refusing a
rewritten candidate outright.

## Older session state (2026-09-03)

- Mac ↔ Windows LAN calls **still fail** after the first mDNS fix. Second pass added:
  candidate rewriting inside the offer/answer **SDP** (not just trickled ICE), and a live
  **diagnostics panel** in the call card (ICE/connection states, candidate counts by type,
  the selected candidate pair) with a copy button. **Ask the user to open it on both sides
  and paste the output** — that is the fastest way to find the real blocker now.
- Added: screen sharing, custom Windows title bar (app is frameless on Windows now),
  sidebar footer height matched to the composer.
- **Next step (user):** re-test calls + screen share across two machines.

## Screen sharing

The viewer's **"expand" fills the app window; it deliberately does not use the native
Fullscreen API** — `requestFullscreen` is a no-op in WKWebView and blacked out the entire
window in WebView2. The panel is centred by a flex layer (`.screen-layer`) rather than a
CSS transform, because framer-motion owns `transform` for dragging. `.screen-stage` holds
`aspect-ratio: 16/9` so the panel keeps its shape before the first frame, and a spinner
placeholder shows until `videoWidth > 0`. Panel width is drag-resizable from the corner and
persisted in `localStorage` (`cloudix:screen-width`).

`getDisplayMedia` at 1080p60 with `contentHint = "detail"`, `maintain-resolution` and an
8 Mbps cap. WebRTC carries no notion of "this track is a screen", so the presenter sends a
`screen-on` signal listing the track ids (`screen-off` on stop). The receiver keeps two
MediaStreams — camera/voice and screen — and `routeTrack()` moves tracks between them;
`receivedTracksRef` lets a late `screen-on` reclassify tracks that already arrived. The
share's audio rides on the screen `<video>` element, which is why its volume slider is
independent of the call volume. Adding/removing tracks triggers `renegotiate()`
(`renegotiate-offer` / `renegotiate-answer`).

## Older session state (2026-09-02)

- Two-machine testing found: **Mac ↔ Windows calls on a plain LAN never connected** —
  offer/answer completed (both sides showed "calling") but ICE never did. Root cause:
  WebViews publish host ICE candidates as `<uuid>.local` **mDNS** names, and macOS WebKit
  and Windows WebView2 can't resolve each other's names. Windows ↔ Windows worked because
  both run the same resolver. **Fixed** by rewriting inbound `.local` candidates with the
  peer's real IP (see "Call ICE" below).
- Also in this pass: liquid-glass redesign, pink theme, platform-aware window chrome,
  emoji picker, draggable non-blocking call window, data-folder/version surfacing.
- **Next step (user):** re-test Mac ↔ Windows and Windows ↔ Windows (RadminVPN) calls.

## Call ICE (why calls used to fail cross-platform)

`app.peerIP(peerID)` resolves a peer's real address — live TCP connection first
(`transport.RemoteIP`), then discovery. Every `signal:incoming` event carries it as
`peerIp`. In `CallModal`, `rewriteMdnsCandidate()` swaps the `.local` hostname in field 4
of an inbound ICE candidate for that IP before `addIceCandidate`. Both peers do this, so
each side ends up with a directly usable host candidate. Keep `peerIpRef` fed from every
signal — don't rely on the seed from discovery alone.

## Stack

- **Backend:** Go 1.25 (`go.mod` says `go 1.25.0`), Wails v2.13, SQLite via
  `modernc.org/sqlite` (pure Go, no cgo). Uses `golang.org/x/sys` (direct) for
  SO_REUSEADDR/REUSEPORT socket control in discovery.
- **Frontend:** React 18 + Vite 5, Framer Motion. Single big component file.
- **Transport:** newline-delimited JSON over TCP (`backend/transport`). Frame cap
  `maxLineBytes` = 96 MiB; oversized frames are skipped, not fatal. 10s write deadline.
- **Discovery:** UDP multicast `239.255.42.99:47990` + unicast fallback on `47990`
  (for VPN tunnels where multicast isn't forwarded). Shared port const `discovery.UDPPort`.
- **Calls:** WebRTC; signaling (offer/answer/ICE) is tunneled through the same TCP transport
  as `signal` envelopes. STUN only (`stun.l.google.com:19302`), no TURN.

## Layout

```
main.go                     Wails bootstrap + window options
backend/app/app.go          All Wails-bound methods + inbound envelope router (handleEnvelope)
backend/transport/          TCP manager: dial/accept, conn pool keyed by peerId, send w/ 1 retry
backend/discovery/          UDP multicast + unicast announce/listen, peer TTL, manual targets
backend/models/models.go    Wire types, envelope + signal kind constants
backend/storage/storage.go  SQLite: profile, chats, messages, blocklist. Migrations = CREATE IF NOT EXISTS + best-effort ALTER
frontend/src/App.jsx        ~2200 lines: entire UI (Onboarding, Sidebar, ChatWindow, CallModal, ProfilePanel, Settings, App)
frontend/src/i18n.js        RU/EN dictionary (incomplete — see gotchas)
frontend/wailsjs/           Generated bindings (committed). Regenerated by wails on build/dev.
```

## Build / run

```bash
wails dev                              # dev mode, hot reload
wails build -platform darwin/universal # -> build/bin/Cloudix.app
wails build -platform windows/amd64    # -> build/bin/Cloudix.exe (build on Windows / CI, no cross-compile)

go build ./...                         # backend compiles (needs frontend/dist to exist for the embed)
cd frontend && npm run build           # frontend only; output frontend/dist is gitignored
```

There is no Go test suite yet. `go vet ./...` is clean.

Run two local instances for P2P testing: give each a separate DB dir via
`CLOUDIX_INSTANCE` (env, honored by `storage.dbPath` → `~/Library/Application Support/
Cloudix-<val>/`), e.g.

```bash
BIN=build/bin/cloudix.app/Contents/MacOS/cloudix
CLOUDIX_INSTANCE=a "$BIN" &
CLOUDIX_INSTANCE=b "$BIN" &
```

Messages/media/etc. work this way; **calls do not connect between two instances on one
machine** (ICE fails — see "Still open"). Real call testing needs two machines.

## Conventions

- Bound method names are PascalCase on `*App`; every one re-checks `a.store != nil` /
  `a.getProfile() != nil` and returns a plain `fmt.Errorf`. Keep that pattern.
- Outbound network sends that can block should run in a goroutine and report failure via
  `a.emitEvent(...)` (see `SendSignal`), NOT by blocking the bound call.
- Frontend talks to Go only through `import * as WailsApp from "../wailsjs/go/app/App"` and
  `EventsOn(...)`. Event names: `peers:update`, `message:incoming|deleted|read|reacted|delivered`,
  `ping:result`, `profile:updated`, `account:deleted`, `signal:incoming`, `signal:send_error`.
- After adding/changing a bound method or a `models.*` struct, regenerate bindings
  (`wails build` does it) and keep `frontend/wailsjs/go/` in sync (`App.js`, `App.d.ts`,
  `models.ts`). `NetworkReady() bool` is bound and used for the connection badge.
- Message delivery: `SendMessage` inserts `delivered=0`, sends in a goroutine
  (`trySendMessage`), marks `delivered=1` + emits `message:delivered` on success.
  `deliveryFlushLoop` (5s ticker, also poked from `onNewPeerDiscovered` and on inbound
  message) retries `ListAllUndelivered` rows only when the peer is live / has an open conn.
- Reactions: `reaction` = mine, `reaction_peer` = theirs. `SetMessageReaction(id, emoji, mine)`.
- New user-facing strings must go through `t.*` in `i18n.js` (both `ru` and `en`).
  **Never store a localized string in the DB** — chat-list media previews are
  locale-neutral tokens (`models.PreviewImage/Video/File`) rendered via
  `previewText(raw, t)`. That was a real bug: Russian previews leaked into the English UI.
- **Themes:** `dark` (default) · `light` · `pink` (pastel, light-only), cycled by
  `ThemeButton` and stamped on `<html data-theme>`. Theme + language persist in
  `localStorage` (`cloudix:theme`, `cloudix:lang`). Adding a theme = add to `THEMES` +
  `THEME_ICON` in `App.jsx`, a `[data-theme="…"]` token block in `theme.css`, and
  `t.theme.<name>`.
- **Platform chrome:** `<html data-platform>` is set from a UA guess before first paint,
  then confirmed via Wails `Environment()`; `App` also keeps it in a `platform` state.
  macOS keeps the native traffic lights (`TitleBarHiddenInset`) and the rounded shell.
  **Windows runs frameless** (`main.go` sets `Frameless: goruntime.GOOS == "windows"`) and
  renders `WindowsTitlebar` — our own bar with minimise/maximise/close wired to the Wails
  runtime. `AppTitlebar` picks the right one and must be used on every root screen
  (app, onboarding, disclaimer) — a frameless window with no bar cannot be closed.
  Corners stay square on Windows: the window is opaque, so CSS rounding shows black.
  Any window-chrome CSS must be gated on `[data-platform=…]` so macOS is unaffected.
- CSS lives entirely in `frontend/src/styles/theme.css`, driven by custom properties on
  `:root` / `[data-theme]`. Use the tokens (`--panel`, `--border`, `--accent`, `--shadow-*`,
  `--ease`), not literal colors, or a new theme will not pick the change up.
- Comments in the code marked `FIX:` / `NEW:` document past bug fixes — leave them.
- Don't commit build artifacts. `/cloudix` and `/cloudix.exe` are gitignored.

## Fixed in the 2026-09-01 audit pass

1. `OnBeforeClose` wired into `wails.Run` (`main.go`) — goodbye announce + clean shutdown.
2. Transport frame cap raised to 96 MiB and switched from `bufio.Scanner` to a
   `bufio.Reader` loop that *skips* an oversized frame instead of killing the connection;
   25 MB attachment guard in the frontend (`MAX_ATTACHMENT_BYTES`).
3. CI (`build.yml`) → Go 1.25, node 20, pinned wails, `windows` + `macos` matrix. README too.
4. `SendMessage` network send moved to a goroutine + 10s write deadline in transport.
6. `discovery` Start/Stop/Restart serialized by `runMu`; loop goroutines take their stop
   channel by value so a Restart cleanly retires the old generation.
7. `unicastListenLoop` now binds via `net.ListenConfig{Control: reuseControl}`
   (SO_REUSEADDR + SO_REUSEPORT on unix, SO_REUSEADDR on windows —
   `reuse_unix.go` / `reuse_windows.go`).
8. Call: only tears down on `failed`; `disconnected` gets an 8s grace timer. Phase is
   also promoted to "connected" via `oniceconnectionstatechange` (fallback for WebViews
   where `RTCPeerConnection.connectionState` isn't implemented — otherwise media connects
   but the UI stays stuck on "calling").
9. Call: `drainPendingIce()` called after every `setRemoteDescription`, incl. the callee.
10. i18n: `en` filled out; RU literals in `App.jsx` routed through `t.*`.
11. Per-user reactions: `reaction` (mine) + `reaction_peer` (theirs) columns.
12. Offline delivery: `delivered` column + `deliveryFlushLoop` retry (see Conventions).
13. Incoming/echoed messages sorted by `ts` and de-duped by `id` in the reducers.
15. `Logout()` removes the `cloudix:saved-messages:<peerId>` localStorage key.
16. `.gitignore` fixed; `/cloudix` untracked (`git rm --cached`) and ignored.
17. `discovery.UDPPort` const used in `app.go` instead of the `"47990"` literal.
18. Connection badge driven by `NetworkReady()`; `initNetworking` failure shows "disconnected".

## Fixed in the 2026-09-02 UX pass

- **Cross-platform calls**: mDNS ICE candidate rewrite (see "Call ICE" above).
- **i18n leak**: media previews are locale-neutral tokens now, not stored Russian strings.
- Liquid-glass redesign of `theme.css` (token-driven, 3 themes, aurora backdrop, springy
  motion throughout).
- Windows chrome: no self-rounding (black corners), no dead 38px mac inset.
- Emoji picker in the composer; `ThemeButton` in the sidebar brand row and onboarding.
- Call window is draggable (`useDragControls` on a handle) and no longer blocks the app —
  `.call-overlay` is `pointer-events: none`, only the card is interactive.
- Sidebar footer: compact Settings + GitHub button, fixed 52px height.
- Settings shows `AppVersion()` and `GetDataDir()` with an `OpenDataFolder()` button, so a
  stale side-by-side install is visible instead of silently confusing.
- Default window 1340×880 (was 1180×760).

## Still open (deferred, by design or scope)

- **5. No sender authentication.** `env.SenderID` is still an unverified string — a
  same-network host can spoof messages/deletes/`account_deleted`/reactions or inject
  `end`/`reject` into a call given the `callId`. Documented as the threat model in README
  (trusted network only). A real fix = per-peer TOFU keypin + envelope signatures.
- **14. No message pagination** — a whole chat (media inline as base64) still loads into
  memory on open. Fine for now; revisit if chats get large.
- `models.SignalPayload.Name/Username` exist but are never populated; the incoming-call
  screen derives the name from discovery/chat metadata instead. Harmless dead fields.
- Pre-existing lockless access to `a.store` / `a.transport` / `a.discovery` across
  goroutines vs `Logout()` nil-assignment. New code snapshots into locals to match the
  existing style; a full fix would need an RWMutex around those fields.
- **Mac ↔ Windows calls still do not connect** (as of 2026-09-03). Signalling completes,
  ICE does not. Both the trickled-candidate and SDP-level mDNS rewrites are in place, so
  the next step is data, not more guessing: have the user open the call diagnostics panel
  on both machines and report `ice-conn`, the candidate counts and the selected pair.
  Things not yet ruled out: Windows Firewall dropping inbound UDP for the app, the two
  hosts being on different subnets/VLANs, or WebKit refusing the rewritten candidates.
- **Calls between two instances on one Mac** may also not reach "connected" — two WebViews
  on one host resolve to the same IP and the loopback path is flaky. Use two machines.
- No TURN server, STUN only — peers behind symmetric NAT on different networks won't
  connect. Fine for the LAN/VPN threat model.
