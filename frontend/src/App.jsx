import React, { useState, useEffect, useRef, useCallback, useMemo } from "react";
import { motion, AnimatePresence, useDragControls, useMotionValue } from "framer-motion";
import { useT, previewText } from "./i18n";
import { can, isMobile } from "./platform";
import * as WailsApp from "../wailsjs/go/app/App";
import {
  EventsOn,
  Environment,
  BrowserOpenURL,
  WindowMinimise,
  WindowToggleMaximise,
  WindowIsMaximised,
  Quit,
} from "../wailsjs/runtime/runtime";

const REACTIONS = ["👍", "❤️", "🔥", "😂", "👎"];
const SAVED_CHAT_ID = "__saved__";
const GITHUB_URL = "https://github.com/Nikerontik/Cloudix";
const savedStorageKey = (peerId) => "cloudix:saved-messages:" + (peerId || "anon");
// Keep in sync with transport.maxLineBytes on the Go side (base64 inflates ~33%).
const MAX_ATTACHMENT_BYTES = 25 * 1024 * 1024;

// dark -> light -> pink -> dark. "pink" is a light-only pastel theme.
const THEMES = ["dark", "light", "pink"];
const THEME_ICON = { dark: "🌙", light: "☀️", pink: "🌸" };
const nextTheme = (current) =>
  THEMES[(THEMES.indexOf(current) + 1 + THEMES.length) % THEMES.length];

const EMOJIS = [
  "😀", "😂", "🥲", "😊", "😍", "🤔", "😎", "🙃",
  "😴", "🤯", "😭", "😡", "🥳", "🤝", "🙏", "👏",
  "👍", "👎", "❤️", "🔥", "✨", "🎉", "💯", "👀",
  "🚀", "☕", "🍕", "🎮", "🎧", "💻", "📎", "✅",
];

const sortByTs = (arr) =>
  [...arr].sort((a, b) => (a?.ts || 0) - (b?.ts || 0));

const readStored = (key, fallback, allowed) => {
  try {
    const v = localStorage.getItem(key);
    return v && (!allowed || allowed.includes(v)) ? v : fallback;
  } catch {
    return fallback;
  }
};

// ---------------------------------------------------------------- screen ---
// Screen-share encoding profile. Defaults target a LAN: 1080p60 with a high
// ceiling, because the visible problems were bitrate starvation (blocky video)
// and the encoder trading framerate away under load.
const SCREEN_HEIGHTS = [720, 1080, 1440];
const SCREEN_FPS = [15, 30, 60];
const SCREEN_MODES = ["balanced", "detail", "motion"];
const SCREEN_QUALITY_KEY = "cloudix:screen-quality";
const SCREEN_QUALITY_EVENT = "cloudix:screen-quality-changed";
const MIC_DEVICE_KEY = "cloudix:mic-device";

const MIC_DEVICE_EVENT = "cloudix:mic-changed";
const RELAY_KEY = "cloudix:relay";

// The relay is user-supplied, never baked into the app — everyone runs their
// own or borrows one they trust. We only remember what was typed last.
function loadRelay() {
  try {
    const raw = JSON.parse(localStorage.getItem(RELAY_KEY) || "{}");
    return { addr: raw.addr || "", token: raw.token || "", use: !!raw.use };
  } catch {
    return { addr: "", token: "", use: false };
  }
}

function saveRelay(relay) {
  try {
    localStorage.setItem(RELAY_KEY, JSON.stringify(relay));
  } catch {}
}

const loadMicDevice = () => readStored(MIC_DEVICE_KEY, "");

function saveMicDevice(id) {
  try {
    localStorage.setItem(MIC_DEVICE_KEY, id);
  } catch {}
  // Lets an active call swap the input without reconnecting.
  window.dispatchEvent(new CustomEvent(MIC_DEVICE_EVENT, { detail: id }));
}

const DEFAULT_SCREEN_QUALITY = {
  height: 1080,
  fps: 60,
  mode: "balanced",
  bitrate: 12,
  // "" = try the system audio getDisplayMedia offers (Windows); "none" = silent;
  // anything else is an audio input device id (macOS needs a loopback driver).
  audioSource: "",
};

function loadScreenQuality() {
  try {
    const raw = JSON.parse(localStorage.getItem(SCREEN_QUALITY_KEY) || "{}");
    return {
      height: SCREEN_HEIGHTS.includes(raw.height) ? raw.height : DEFAULT_SCREEN_QUALITY.height,
      fps: SCREEN_FPS.includes(raw.fps) ? raw.fps : DEFAULT_SCREEN_QUALITY.fps,
      mode: SCREEN_MODES.includes(raw.mode) ? raw.mode : DEFAULT_SCREEN_QUALITY.mode,
      bitrate:
        Number.isFinite(raw.bitrate) && raw.bitrate >= 1 && raw.bitrate <= 30
          ? raw.bitrate
          : DEFAULT_SCREEN_QUALITY.bitrate,
      audioSource:
        typeof raw.audioSource === "string"
          ? raw.audioSource
          : DEFAULT_SCREEN_QUALITY.audioSource,
    };
  } catch {
    return { ...DEFAULT_SCREEN_QUALITY };
  }
}

function saveScreenQuality(quality) {
  try {
    localStorage.setItem(SCREEN_QUALITY_KEY, JSON.stringify(quality));
  } catch {}
  // Lets an in-progress share pick the change up without renegotiating.
  window.dispatchEvent(new CustomEvent(SCREEN_QUALITY_EVENT, { detail: quality }));
}

// "detail" keeps text legible and drops framerate under load; "motion" does the
// opposite; "balanced" lets the encoder decide.
function encodingForQuality(quality) {
  const degradationPreference =
    quality.mode === "detail"
      ? "maintain-resolution"
      : quality.mode === "motion"
        ? "maintain-framerate"
        : "balanced";
  return {
    degradationPreference,
    contentHint: quality.mode === "motion" ? "motion" : "detail",
    encoding: {
      maxBitrate: Math.round(quality.bitrate * 1000000),
      maxFramerate: quality.fps,
      networkPriority: "high",
      priority: "high",
    },
  };
}

const fmtBitrate = (bps) =>
  bps >= 1000000 ? `${(bps / 1000000).toFixed(1)} Mbps` : `${Math.round(bps / 1000)} kbps`;

// Guess the platform before first paint so Windows never flashes the macOS
// window chrome; Environment() confirms it right after mount.
function guessPlatform() {
  // The mobile bridge loads before this bundle and knows the platform for
  // certain, which UA sniffing does not: iPadOS reports itself as a Mac.
  if (typeof window !== "undefined" && window.__cloudix && window.__cloudix.platform) {
    return window.__cloudix.platform;
  }
  const ua = typeof navigator === "undefined" ? "" : navigator.userAgent;
  if (/android/i.test(ua)) return "android";
  if (/iphone|ipad|ipod/i.test(ua)) return "ios";
  if (/windows|win32|win64/i.test(ua)) return "windows";
  if (/linux|x11/i.test(ua) && !/android/i.test(ua)) return "linux";
  return "darwin";
}

if (typeof document !== "undefined") {
  document.documentElement.setAttribute("data-platform", guessPlatform());
}

// Our own window chrome for Windows, where the app runs frameless (see
// main.go). macOS keeps its native traffic lights and never renders this.
function WindowsTitlebar({ t }) {
  const [maximized, setMaximized] = useState(false);

  useEffect(() => {
    let alive = true;
    const sync = () => {
      WindowIsMaximised()
        .then((v) => alive && setMaximized(!!v))
        .catch(() => {});
    };
    sync();
    window.addEventListener("resize", sync);
    return () => {
      alive = false;
      window.removeEventListener("resize", sync);
    };
  }, []);

  const toggle = () => {
    WindowToggleMaximise();
    setMaximized((v) => !v);
  };

  return (
    <div className="win-titlebar">
      <div className="win-titlebar-drag" onDoubleClick={toggle} />
      <div className="win-controls">
        <button
          type="button"
          className="win-btn"
          title={t.win.minimize}
          aria-label={t.win.minimize}
          onClick={() => WindowMinimise()}
        >
          <svg viewBox="0 0 10 10" aria-hidden="true">
            <rect x="0" y="4.5" width="10" height="1" fill="currentColor" />
          </svg>
        </button>
        <button
          type="button"
          className="win-btn"
          title={maximized ? t.win.restore : t.win.maximize}
          aria-label={maximized ? t.win.restore : t.win.maximize}
          onClick={toggle}
        >
          {maximized ? (
            <svg viewBox="0 0 10 10" aria-hidden="true">
              <rect x="0" y="2.5" width="7.5" height="7.5" fill="none" stroke="currentColor" />
              <path d="M2.5 2.5V0H10v7.5H7.5" fill="none" stroke="currentColor" />
            </svg>
          ) : (
            <svg viewBox="0 0 10 10" aria-hidden="true">
              <rect x="0.5" y="0.5" width="9" height="9" fill="none" stroke="currentColor" />
            </svg>
          )}
        </button>
        <button
          type="button"
          className="win-btn close"
          title={t.win.close}
          aria-label={t.win.close}
          onClick={() => Quit()}
        >
          <svg viewBox="0 0 10 10" aria-hidden="true">
            <path d="M0 0L10 10M10 0L0 10" stroke="currentColor" fill="none" />
          </svg>
        </button>
      </div>
    </div>
  );
}

// Renders the right chrome for the platform: our bar on Windows, the spacer
// that clears the mac traffic lights everywhere else.
function AppTitlebar({ platform, t, className = "titlebar" }) {
  // A phone has no window to minimise, maximise or close, and no traffic-light
  // inset to leave room for.
  if (platform === "ios" || platform === "android") return null;
  if (platform === "windows") return <WindowsTitlebar t={t} />;
  return <div className={className} />;
}

// WebViews hide local IPs behind "<uuid>.local" mDNS ICE candidates. macOS
// WebKit and Windows WebView2 fail to resolve each other's names, so a
// Mac<->Windows LAN call gathers candidates but never connects. We already know
// the peer's real address from the TCP transport, so swap it in.
function rewriteCandidateLine(line, peerIp) {
  const parts = line.split(" ");
  // candidate:<foundation> <component> <proto> <priority> <address> <port> typ ...
  if (parts.length < 6 || !/\.local$/i.test(parts[4])) return line;
  parts[4] = peerIp;
  return parts.join(" ");
}

// Candidates also ride inside the offer/answer SDP, not just in trickled ICE
// messages, so the same de-obfuscation has to happen there.
function rewriteSdpMdns(desc, peerIp) {
  if (!peerIp || !desc || typeof desc.sdp !== "string") return desc;
  if (!/\.local/i.test(desc.sdp)) return desc;
  const sdp = desc.sdp
    .split(/\r?\n/)
    .map((line) =>
      line.startsWith("a=candidate:")
        ? "a=" + rewriteCandidateLine(line.slice(2), peerIp)
        : line
    )
    .join("\r\n");
  return { ...desc, sdp };
}

function rewriteMdnsCandidate(init, peerIp) {
  if (!peerIp || !init || typeof init.candidate !== "string") return init;
  return { ...init, candidate: rewriteCandidateLine(init.candidate, peerIp) };
}

// Re-binds the discovery sockets and re-polls peers. Multicast announces get
// lost often enough (interface changes, sleep, VPN adapters) that a manual
// "find people again" is worth a button.
function RescanButton({ t, onRescan }) {
  const [busy, setBusy] = useState(false);

  const run = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await onRescan();
    } finally {
      // Keep the spin visible long enough to read as feedback.
      setTimeout(() => setBusy(false), 700);
    }
  };

  return (
    <motion.button
      type="button"
      className={"icon-btn " + (busy ? "spinning" : "")}
      title={busy ? t.rescanning : t.rescan}
      aria-label={t.rescan}
      onClick={run}
      disabled={busy}
      whileTap={{ scale: 0.86 }}
    >
      <svg viewBox="0 0 16 16" width="15" height="15" fill="none" aria-hidden="true">
        <path
          d="M14 8a6 6 0 1 1-1.76-4.24"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
        />
        <path
          d="M14 2v4h-4"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </motion.button>
  );
}

function ThemeButton({ theme, setTheme, t, className = "" }) {
  return (
    <motion.button
      type="button"
      className={"icon-btn " + className}
      title={t.theme.toggle + " · " + t.theme[theme]}
      aria-label={t.theme.toggle}
      onClick={() => setTheme(nextTheme(theme))}
      whileTap={{ scale: 0.86, rotate: -18 }}
    >
      <AnimatePresence mode="wait" initial={false}>
        <motion.span
          key={theme}
          initial={{ opacity: 0, rotate: -70, scale: 0.5 }}
          animate={{ opacity: 1, rotate: 0, scale: 1 }}
          exit={{ opacity: 0, rotate: 70, scale: 0.5 }}
          transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
          style={{ display: "block", lineHeight: 1 }}
        >
          {THEME_ICON[theme]}
        </motion.span>
      </AnimatePresence>
    </motion.button>
  );
}

function Avatar({ name, avatar, size = "", onClick, online }) {
  return (
    <div
      className={"avatar-wrap " + size}
      onClick={onClick}
      style={onClick ? { cursor: "pointer" } : undefined}
    >
      <div className={"avatar " + size}>
        {avatar ? <img src={avatar} alt="" /> : (name?.[0]?.toUpperCase() || "?")}
      </div>
      {online && <span className="online-dot-badge" />}
    </div>
  );
}

function Onboarding({ onDone, t, theme, setTheme, platform }) {
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [bio, setBio] = useState("");
  const [avatar, setAvatar] = useState(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fileRef = useRef(null);

  const pickAvatar = (e) => {
    const file = e.target.files?.[0];
    if (!file) return;
    if (file.size > MAX_ATTACHMENT_BYTES) {
      setError(t.attachTooLarge);
      return;
    }
    const reader = new FileReader();
    reader.onload = () => setAvatar(reader.result);
    reader.readAsDataURL(file);
  };

  const canContinue = name.trim().length > 0 && username.trim().length > 1 && !busy;

  const finish = async () => {
    if (!canContinue) return;
    setBusy(true);
    setError("");
    const uname = username.trim().startsWith("@") ? username.trim() : "@" + username.trim();

    try {
      const profile = await WailsApp.Register(name.trim(), uname, bio.trim(), avatar || "");
      onDone(profile);
    } catch (err) {
      console.error("Register failed:", err);
      setError(t.onboarding.error);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="onboarding-root">
      <AppTitlebar platform={platform} t={t} className="onboarding-titlebar" />
      <div className="onboarding-theme">
        <ThemeButton theme={theme} setTheme={setTheme} t={t} />
      </div>
      <motion.div
        className="onboarding-card"
        initial={{ opacity: 0, y: 24, scale: 0.96 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: 0.45, ease: [0.22, 1, 0.36, 1] }}
      >
        <motion.div
          className="onboarding-logo"
          animate={{ y: [0, -8, 0] }}
          transition={{ duration: 3.2, repeat: Infinity, ease: "easeInOut" }}
        >
          <svg viewBox="0 0 200 200" width="140" height="140" aria-hidden="true">
            <defs>
              <linearGradient id="cloudGrad" x1="0" y1="0" x2="1" y2="1">
                <stop offset="0%" stopColor="#5fc4ff" />
                <stop offset="100%" stopColor="#0a84ff" />
              </linearGradient>
            </defs>
            <circle cx="100" cy="100" r="96" fill="url(#cloudGrad)" />
            <path
              d="M60 118c-12 0-22-9-22-21 0-11 8-19 18-21 3-14 15-24 30-24 13 0 24 8 28 20 13 1 23 12 23 25 0 13-11 24-24 24H60z"
              fill="white"
              opacity="0.95"
            />
          </svg>
        </motion.div>

        <h1 className="onboarding-title">Cloudix</h1>
        <p className="onboarding-sub">{t.onboarding.subtitle}</p>

        <div
          className="onboarding-avatar-pick"
          onClick={() => fileRef.current?.click()}
          role="button"
          tabIndex={0}
          onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && fileRef.current?.click()}
        >
          {avatar ? <img src={avatar} alt="" /> : <span>+</span>}
        </div>

        <input
          ref={fileRef}
          type="file"
          accept="image/*"
          style={{ display: "none" }}
          onChange={pickAvatar}
        />

        <div className="onboarding-field">
          <label>{t.onboarding.name}</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t.onboarding.namePlaceholder}
            autoFocus
          />
        </div>

        <div className="onboarding-field">
          <label>{t.onboarding.username}</label>
          <input
            type="text"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder={t.onboarding.usernamePlaceholder}
          />
        </div>

        <div className="onboarding-field">
          <label>{t.onboarding.bio}</label>
          <input
            type="text"
            value={bio}
            onChange={(e) => setBio(e.target.value)}
            placeholder={t.onboarding.bioPlaceholder}
          />
        </div>

        {error && <div className="onboarding-error">{error}</div>}

        <motion.button
          type="button"
          className="onboarding-btn"
          disabled={!canContinue}
          whileTap={{ scale: 0.96 }}
          onClick={finish}
        >
          {busy ? t.onboarding.creating : t.onboarding.start}
        </motion.button>
      </motion.div>
    </div>
  );
}

function DisclaimerModal({ onDismiss, t, platform }) {
  return (
    <div className="onboarding-root">
      <AppTitlebar platform={platform} t={t} className="onboarding-titlebar" />
      <motion.div
        className="onboarding-card"
        initial={{ opacity: 0, y: 24, scale: 0.96 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: 0.45, ease: [0.22, 1, 0.36, 1] }}
      >
        <h1 className="onboarding-title">Cloudix</h1>
        <p className="onboarding-sub">{t.disclaimer.text}</p>
        <motion.button
          type="button"
          className="onboarding-btn"
          whileTap={{ scale: 0.96 }}
          onClick={onDismiss}
        >
          {t.disclaimer.ok}
        </motion.button>
      </motion.div>
    </div>
  );
}

function Sidebar({
  chats,
  onlinePeers,
  activeChat,
  setActiveChat,
  tab,
  setTab,
  onOpenSettings,
  onOpenProfile,
  t,
  myProfile,
  search,
  setSearch,
  onStartChatWithPeer,
  typingByPeer,
  theme,
  setTheme,
  onRescan,
  onOpenNetwork,
  onOpenDocs,
  netActive,
}) {
  // FIX: убраны вкладки "groups"/"channels" по запросу — они никогда не были
  // реализованы и только занимали место. "saved" тоже убрана как отдельная
  // вкладка — теперь это закреплённый чат сверху списка (см. App: savedChatMeta).
  const tabsOrder = can("lanDiscovery") ? ["all", "online"] : ["all"];

  // Unread across every chat, so the tab shows new messages even while the
  // "Online" tab is the one being looked at.
  const totalUnread = (Array.isArray(chats) ? chats : []).reduce(
    (sum, c) => sum + (c?.unread || 0),
    0
  );

  const filtered = (Array.isArray(chats) ? chats : []).filter((c) => {
    const title = (c?.title || "").toLowerCase();
    const q = (search || "").toLowerCase();
    const matchesSearch = q ? title.includes(q) : true;
    if (!matchesSearch) return false;
    if (tab === "all") return true;
    return true;
  });

  return (
    <div className="sidebar glass">
      {/* Brand row sits below the mac traffic-light inset, so it never overlaps
          the window controls. */}
      <div className="sidebar-brand">
        <span className="brand-title">{t.appName}</span>
        <div className="brand-actions">
          <RescanButton t={t} onRescan={onRescan} />
          <ThemeButton theme={theme} setTheme={setTheme} t={t} />
        </div>
      </div>

      <div className="sidebar-header">
        <Avatar
          name={myProfile?.name}
          avatar={myProfile?.avatar}
          onClick={() => onOpenProfile(myProfile, true)}
        />
        <div className="sidebar-search">
          <span className="search-icon">⌕</span>
          <input
            placeholder={t.searchPlaceholder}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
      </div>

      <div className="sidebar-tabs">
        {tabsOrder.map((tb) => (
          <button
            key={tb}
            type="button"
            className={"tab-btn " + (tab === tb ? "active" : "")}
            onClick={() => setTab(tb)}
          >
            {t.tabs[tb]}
            {tb === "online" && onlinePeers.length > 0 && (
              <span className="tab-count">{onlinePeers.length}</span>
            )}
            {tb === "all" && totalUnread > 0 && (
              <span className="tab-count unread">
                {totalUnread > 99 ? "99+" : totalUnread}
              </span>
            )}
          </button>
        ))}
      </div>

      {tab === "online" ? (
        <div className="chat-list">
          {onlinePeers.length === 0 && (
            <div className="empty-hint">
              {t.noPeersTitle}
              <br />
              {t.noPeersHint}
            </div>
          )}
          {onlinePeers.map((p) => (
            <motion.div
              key={p.peerId}
              className="chat-item"
              whileTap={{ scale: 0.98 }}
              onClick={() => onStartChatWithPeer(p)}
            >
              <Avatar name={p.name} avatar={p.avatar} online />
              <div className="chat-meta">
                <div className="chat-title">{p.name}</div>
                <div className="chat-preview">
                  {p.username} · {p.viaVpn ? t.viaVpn : t.foundInNetwork}
                </div>
              </div>
            </motion.div>
          ))}
        </div>
      ) : (
        <div className="chat-list">
          {filtered.length === 0 && (
            <div className="empty-hint">
              {t.noChatsTitle}
              <br />
              {t.noChatsHint}
            </div>
          )}
          {filtered.map((c) => {
            const isPinned = c.id === SAVED_CHAT_ID;
            const isTyping = !isPinned && typingByPeer && typingByPeer[c.peerId];
            return (
              <motion.div
                key={c.id}
                className={
                  "chat-item " +
                  (isPinned ? "pinned " : "") +
                  (activeChat === c.id ? "active" : "")
                }
                onClick={() => setActiveChat(c.id)}
                whileTap={{ scale: 0.98 }}
              >
                <Avatar
                  name={c.title}
                  avatar={c.avatar}
                  online={c.online}
                  onClick={
                    isPinned
                      ? undefined
                      : (e) => {
                          e.stopPropagation();
                          onOpenProfile(c, false);
                        }
                  }
                />
                <div className="chat-meta">
                  <div className="chat-title">
                    {isPinned && <span className="pin-icon">📌 </span>}
                    {c.title}
                    {c.deleted && (
                      <span className="deleted-tag"> · {t.accountDeletedTag}</span>
                    )}
                  </div>
                  <div className={"chat-preview " + (isTyping ? "typing-preview" : "")}>
                    {isTyping
                      ? t.typing
                      : c.preview
                        ? previewText(c.preview, t)
                        : t.chatPreviewEmpty}
                  </div>
                </div>
                {c.unread > 0 && (
                  <span className="unread-badge">{c.unread > 99 ? "99+" : c.unread}</span>
                )}
              </motion.div>
            );
          })}
        </div>
      )}

      <div className="sidebar-footer">
        <button type="button" className="footer-btn" onClick={onOpenSettings} title={t.settingsBtn}>
          ⚙<span className="footer-btn-label">{t.settingsBtn}</span>
        </button>
        <button
          type="button"
          className="footer-btn icon-only"
          title={t.docs.button}
          aria-label={t.docs.button}
          onClick={onOpenDocs}
        >
          <svg viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <circle cx="8" cy="8" r="6.5" stroke="currentColor" strokeWidth="1.4" />
            <path
              d="M6.2 6.1a1.9 1.9 0 1 1 2.3 2.2c-.4.1-.6.4-.6.8v.4"
              stroke="currentColor"
              strokeWidth="1.4"
              strokeLinecap="round"
            />
            <circle cx="8" cy="11.6" r="0.85" fill="currentColor" />
          </svg>
        </button>
        <button
          type="button"
          className={"footer-btn icon-only " + (netActive ? "net-on" : "")}
          title={t.net.button}
          aria-label={t.net.button}
          onClick={onOpenNetwork}
        >
          <svg viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <circle cx="8" cy="8" r="6.5" stroke="currentColor" strokeWidth="1.4" />
            <path d="M1.5 8h13" stroke="currentColor" strokeWidth="1.4" />
            <path
              d="M8 1.5c1.8 1.9 2.8 4.1 2.8 6.5S9.8 12.6 8 14.5C6.2 12.6 5.2 10.4 5.2 8S6.2 3.4 8 1.5Z"
              stroke="currentColor"
              strokeWidth="1.4"
            />
          </svg>
        </button>
        <button
          type="button"
          className="footer-btn icon-only"
          title={t.githubBtn}
          aria-label={t.githubBtn}
          onClick={() => BrowserOpenURL(GITHUB_URL)}
        >
          <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
            <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
          </svg>
        </button>
      </div>
    </div>
  );
}

// The overlay network panel: create a network or join one by invite. Opened
// from the sidebar footer, next to Settings and GitHub.
function NetworkPanel({ status, t, onClose, onRefresh }) {
  const [tab, setTab] = useState(status?.active ? "status" : can("networkHosting") ? "create" : "join");
  const [mode, setMode] = useState("invite");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [invite, setInvite] = useState("");
  const [addr, setAddr] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");
  const [relay, setRelay] = useState(loadRelay);

  const useRelay = relay.use;
  const relayArgs = useRelay ? [relay.addr.trim(), relay.token] : ["", ""];

  const updateRelay = (patch) => {
    const next = { ...relay, ...patch };
    setRelay(next);
    saveRelay(next);
  };

  useEffect(() => {
    if (status?.active) setTab("status");
  }, [status?.active]);

  const run = async (fn) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      await onRefresh();
    } catch (err) {
      setError(String(err?.message || err));
    } finally {
      setBusy(false);
    }
  };

  const copy = (value, key) => {
    navigator.clipboard?.writeText(value).then(
      () => {
        setCopied(key);
        setTimeout(() => setCopied(""), 1600);
      },
      () => {}
    );
  };

  const active = !!status?.active;
  const members = status?.members || [];

  return (
    <motion.div
      className="profile-overlay"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      onClick={onClose}
      style={{ justifyContent: "center", alignItems: "center" }}
    >
      <motion.div
        className="net-panel glass-strong"
        initial={{ opacity: 0, y: 16, scale: 0.97 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        exit={{ opacity: 0, y: 10, scale: 0.97 }}
        transition={{ type: "spring", stiffness: 300, damping: 28 }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="net-head">
          <div className="net-title">{t.net.title}</div>
          <button type="button" className="screen-btn" onClick={onClose} aria-label={t.mediaViewer.close}>
            ✕
          </button>
        </div>

        <div className="net-body">
          {!active && (
            <>
              <p className="net-sub">{t.net.subtitle}</p>

              {can("networkHosting") && (
                <div className="sidebar-tabs net-tabs">
                  <button
                    type="button"
                    className={"tab-btn " + (tab === "create" ? "active" : "")}
                    onClick={() => setTab("create")}
                  >
                    {t.net.tabCreate}
                  </button>
                  <button
                    type="button"
                    className={"tab-btn " + (tab === "join" ? "active" : "")}
                    onClick={() => setTab("join")}
                  >
                    {t.net.tabJoin}
                  </button>
                </div>
              )}

              <div className="sidebar-tabs net-tabs">
                <button
                  type="button"
                  className={"tab-btn " + (!useRelay ? "active" : "")}
                  onClick={() => updateRelay({ use: false })}
                >
                  {t.net.direct}
                </button>
                <button
                  type="button"
                  className={"tab-btn " + (useRelay ? "active" : "")}
                  onClick={() => updateRelay({ use: true })}
                >
                  {t.net.viaRelay}
                </button>
              </div>
              <div className="net-hint">
                {useRelay ? t.net.relayHint : t.net.directHint}
              </div>

              {useRelay && (
                <>
                  <label className="net-field">
                    <span>{t.net.relayAddr}</span>
                    <input
                      type="text"
                      value={relay.addr}
                      placeholder={t.net.relayAddrPlaceholder}
                      onChange={(e) => updateRelay({ addr: e.target.value })}
                    />
                  </label>
                  <label className="net-field">
                    <span>{t.net.relayToken}</span>
                    <input
                      type="password"
                      value={relay.token}
                      placeholder={t.net.relayTokenPlaceholder}
                      onChange={(e) => updateRelay({ token: e.target.value })}
                    />
                  </label>
                  <div className="net-hint">{t.net.relayTokenHint}</div>
                </>
              )}

              <label className="net-field">
                <span>{t.net.password}</span>
                <input
                  type="password"
                  value={password}
                  placeholder={t.net.passwordPlaceholder}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </label>

              {tab === "create" ? (
                <>
                  <label className="net-field">
                    <span>{t.net.name}</span>
                    <input
                      type="text"
                      value={name}
                      placeholder={t.net.namePlaceholder}
                      onChange={(e) => setName(e.target.value)}
                    />
                  </label>
                  <button
                    type="button"
                    className="onboarding-btn net-action"
                    disabled={
                      busy ||
                      !name.trim() ||
                      password.length < 8 ||
                      (useRelay && !relay.addr.trim())
                    }
                    onClick={() =>
                      run(() => WailsApp.VPNCreate(name.trim(), password, ...relayArgs))
                    }
                  >
                    {busy ? t.net.creating : t.net.create}
                  </button>
                </>
              ) : (
                <>
                  {/* Over a relay both sides meet at the same server, so there
                      is no host address to enter — only the network name. */}
                  {useRelay ? (
                    <label className="net-field">
                      <span>{t.net.name}</span>
                      <input
                        type="text"
                        value={name}
                        placeholder={t.net.namePlaceholder}
                        onChange={(e) => setName(e.target.value)}
                      />
                    </label>
                  ) : (
                    <>
                      <div className="sidebar-tabs net-tabs">
                        <button
                          type="button"
                          className={"tab-btn " + (mode === "invite" ? "active" : "")}
                          onClick={() => setMode("invite")}
                        >
                          {t.net.byInvite}
                        </button>
                        <button
                          type="button"
                          className={"tab-btn " + (mode === "manual" ? "active" : "")}
                          onClick={() => setMode("manual")}
                        >
                          {t.net.byAddress}
                        </button>
                      </div>

                      {mode === "invite" ? (
                        <label className="net-field">
                          <span>{t.net.invite}</span>
                          <input
                            type="text"
                            value={invite}
                            placeholder={t.net.invitePlaceholder}
                            onChange={(e) => setInvite(e.target.value)}
                          />
                        </label>
                      ) : (
                        <>
                          <label className="net-field">
                            <span>{t.net.name}</span>
                            <input
                              type="text"
                              value={name}
                              placeholder={t.net.namePlaceholder}
                              onChange={(e) => setName(e.target.value)}
                            />
                          </label>
                          <label className="net-field">
                            <span>{t.net.address}</span>
                            <input
                              type="text"
                              value={addr}
                              placeholder={t.net.addressPlaceholder}
                              onChange={(e) => setAddr(e.target.value)}
                            />
                          </label>
                        </>
                      )}
                    </>
                  )}

                  <button
                    type="button"
                    className="onboarding-btn net-action"
                    disabled={
                      busy ||
                      !password ||
                      (useRelay
                        ? !relay.addr.trim() || !name.trim()
                        : mode === "invite"
                          ? !invite.trim()
                          : !name.trim() || !addr.trim())
                    }
                    onClick={() =>
                      run(() =>
                        useRelay
                          ? WailsApp.VPNJoin(name.trim(), password, "", ...relayArgs)
                          : mode === "invite"
                            ? WailsApp.VPNJoinByInvite(invite.trim(), password, "")
                            : WailsApp.VPNJoin(name.trim(), password, addr.trim(), "", "")
                      )
                    }
                  >
                    {busy ? t.net.joining : t.net.join}
                  </button>
                </>
              )}
            </>
          )}

          {active && (
            <>
              <div className="net-status-row">
                <span className="net-live">●</span>
                <div>
                  <div className="net-network-name">{status.network}</div>
                  <div className="net-role">
                    {status.role === "host" ? t.net.roleHost : t.net.roleMember}
                    {" · "}
                    {status.transport === "relay"
                      ? `${t.net.viaLabel}${status.relayAddr ? " " + status.relayAddr : ""}`
                      : t.net.directLabel}
                  </div>
                </div>
              </div>

              {status.role === "host" && status.transport === "relay" && (
                <div className="net-block">
                  <div className="net-hint">{t.net.relaySecurity}</div>
                  <div className="net-hint">
                    {t.net.name}: <b>{status.network}</b> — {t.net.shareHint}
                  </div>
                </div>
              )}

              {status.role === "host" && status.transport !== "relay" && (
                <div className="net-block">
                  {status.invite ? (
                    <>
                      <div className="net-label">{t.net.invite}</div>
                      <div className="net-invite">
                        <code>{status.invite}</code>
                        <button
                          type="button"
                          className="theme-toggle"
                          onClick={() => copy(status.invite, "invite")}
                        >
                          {copied === "invite" ? t.net.copied : t.net.copy}
                        </button>
                      </div>
                      <div className="net-hint">{t.net.shareHint}</div>
                      <div className="net-hint">
                        {status.portMapped ? t.net.portMapped : t.net.portManual}
                      </div>
                    </>
                  ) : (
                    <div className="net-hint">
                      {status.publicAddr ? t.net.noAddr : t.net.waitingAddr}
                    </div>
                  )}
                </div>
              )}

              <div className="net-block">
                <div className="net-label">
                  {t.net.members} · {members.length}
                </div>
                <div className="net-members">
                  {members.map((m) => (
                    <div key={m.peerId} className="net-member">
                      <Avatar name={m.name || m.peerId} avatar="" online />
                      <div className="chat-meta">
                        <div className="chat-title">{m.name || m.peerId}</div>
                        <div className="chat-preview">
                          {m.isHost ? t.net.host : m.username || m.peerId}
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
              </div>

              <button
                type="button"
                className="theme-toggle danger net-action"
                disabled={busy}
                onClick={() => run(() => WailsApp.VPNLeave())}
              >
                {t.net.leave}
              </button>
            </>
          )}

          {(error || status?.error) && (
            <div className="onboarding-error">{error || status.error}</div>
          )}

          <div className="net-block">
            <div className="net-label">{t.net.fingerprint}</div>
            <div className="net-invite">
              <code>{status?.fingerprint || "…"}</code>
              <button
                type="button"
                className="theme-toggle"
                onClick={() => copy(status?.fingerprint || "", "fp")}
              >
                {copied === "fp" ? t.net.copied : t.net.copy}
              </button>
            </div>
            <div className="net-hint">{t.net.fingerprintHint}</div>
          </div>

          <div className="net-hint net-note">🔒 {t.net.security}</div>
          <div className="net-hint net-note">🖧 {t.net.relayOwn}</div>
          <div className="net-hint net-note">ℹ️ {t.net.notLan}</div>
        </div>
      </motion.div>
    </motion.div>
  );
}

// Plain-language explanation of what the app does and what its security rests
// on, reachable from the sidebar footer.
function DocsPanel({ t, onClose }) {
  return (
    <motion.div
      className="profile-overlay"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      onClick={onClose}
      style={{ justifyContent: "center", alignItems: "center" }}
    >
      <motion.div
        className="net-panel docs-panel glass-strong"
        initial={{ opacity: 0, y: 16, scale: 0.97 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        exit={{ opacity: 0, y: 10, scale: 0.97 }}
        transition={{ type: "spring", stiffness: 300, damping: 28 }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="net-head">
          <div className="net-title">{t.docs.title}</div>
          <button
            type="button"
            className="screen-btn"
            onClick={onClose}
            aria-label={t.docs.close}
          >
            ✕
          </button>
        </div>

        <div className="net-body docs-body">
          {t.docs.sections.map((section, i) => (
            <section key={i} className="docs-section">
              <h3>{section.h}</h3>
              <p>{section.p}</p>
            </section>
          ))}
        </div>
      </motion.div>
    </motion.div>
  );
}

function ConnectionBadge({ status, t }) {
  return (
    <div className="connection-badge">
      <span className={"dot " + status}></span>
      {t.connStatus[status]}
    </div>
  );
}

// NEW: полноэкранный просмотр фото/видео с возможностью скачивания.
// Используется и из чата (клик по вложению), и из вкладки "Медиа".
function MediaViewer({ item, onClose, t }) {
  if (!item) return null;
  const isVideo = item.mediaKind === "video";
  const fileName = "cloudix-media-" + (item.id || Date.now()) + (isVideo ? ".webm" : ".png");

  return (
    <motion.div
      className="media-viewer-overlay"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      onClick={onClose}
    >
      <motion.div
        className="media-viewer-content glass-strong"
        initial={{ scale: 0.9 }}
        animate={{ scale: 1 }}
        exit={{ scale: 0.9 }}
        onClick={(e) => e.stopPropagation()}
      >
        {isVideo ? (
          <video src={item.mediaData} controls autoPlay className="media-viewer-media" />
        ) : (
          <img src={item.mediaData} alt="" className="media-viewer-media" />
        )}
        <div className="media-viewer-actions">
          <button
            type="button"
            className="theme-toggle"
            onClick={async () => {
              try {
                const path = await WailsApp.SaveMedia(fileName, item.mediaData);
                if (path) console.log(t.mediaViewer.saved + path);
              } catch (err) {
                console.error("SaveMedia failed:", err);
                alert(t.mediaViewer.saveError);
              }
            }}
          >
            ⬇ {t.mediaViewer.download}
          </button>
          <button type="button" className="theme-toggle" onClick={onClose}>
            {t.mediaViewer.close}
          </button>
        </div>
      </motion.div>
    </motion.div>
  );
}

function MediaPanel({ messages, onClose, onOpenMedia, t }) {
  const media = (Array.isArray(messages) ? messages : []).filter((m) => m.mediaKind);

  return (
    <motion.div
      className="profile-overlay"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      onClick={onClose}
    >
      <motion.div
        className="profile-panel glass-strong"
        initial={{ x: 360 }}
        animate={{ x: 0 }}
        exit={{ x: 360 }}
        transition={{ type: "spring", stiffness: 260, damping: 26 }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="profile-header">
          <div className="profile-back" onClick={onClose}>
            ‹ {t.profile.back}
          </div>
          <div style={{ fontWeight: 600 }}>{t.profile.mediaTitle}</div>
        </div>
        <div className="media-grid">
          {media.length === 0 && <div className="empty-hint">{t.profile.noMedia}</div>}
          {media.map((m) => (
            <div
              key={m.id}
              className="media-item"
              onClick={() => onOpenMedia && onOpenMedia(m)}
              style={{ cursor: "pointer" }}
            >
              {m.mediaKind === "video" ? (
                <video src={m.mediaData} muted />
              ) : (
                <img src={m.mediaData} alt="" />
              )}
            </div>
          ))}
        </div>
      </motion.div>
    </motion.div>
  );
}

const ICE_KEY = "cloudix:ice";

const DEFAULT_STUN = [
  "stun:stun.l.google.com:19302",
  "stun:stun1.l.google.com:19302",
  "stun:stun.cloudflare.com:3478",
];

// Browsers reject an RTCPeerConnection outright if an ICE URL is malformed, so
// a stray address would break every call rather than just TURN. Accept a bare
// "host:port" and add the scheme.
function normalizeTurnUrl(raw) {
  const v = (raw || "").trim();
  if (!v) return "";
  if (/^(turns?|stuns?):/i.test(v)) return v;
  return "turn:" + v;
}

function loadIceConfig() {
  try {
    const raw = JSON.parse(localStorage.getItem(ICE_KEY) || "{}");
    return {
      turnUrl: raw.turnUrl || "",
      turnUser: raw.turnUser || "",
      turnPass: raw.turnPass || "",
    };
  } catch {
    return { turnUrl: "", turnUser: "", turnPass: "" };
  }
}

function saveIceConfig(cfg) {
  try {
    localStorage.setItem(ICE_KEY, JSON.stringify(cfg));
  } catch {}
}

// STUN only tells a peer its own public address; it cannot join two peers whose
// routers both refuse unsolicited traffic. When both sides are behind
// carrier-grade NAT — increasingly the norm — the media has to be relayed by a
// TURN server, which the user supplies just like the network relay.
function buildRtcConfig() {
  const cfg = loadIceConfig();
  const servers = [{ urls: DEFAULT_STUN }];
  const turn = normalizeTurnUrl(cfg.turnUrl);
  if (turn) {
    servers.push({
      urls: turn,
      username: cfg.turnUser,
      credential: cfg.turnPass,
    });
  }
  return { iceServers: servers };
}

function CallModal({
  target,
  video,
  isCaller,
  callId,
  incomingOffer,
  registerSignalHandler,
  onClose,
  t,
}) {
  const [phase, setPhase] = useState(isCaller ? "calling" : "ringing");
  const [muted, setMuted] = useState(false);
  const [seconds, setSeconds] = useState(0);
  const [remoteHasVideo, setRemoteHasVideo] = useState(false);
  const [errorText, setErrorText] = useState("");
  const [remoteZoomed, setRemoteZoomed] = useState(false);
  const [remoteVolume, setRemoteVolume] = useState(1);
  const remoteVolumeRef = useRef(1);
  const dragControls = useDragControls();
  const screenDragControls = useDragControls();

  // Screen share: `sharing` = we are the presenter, `remoteScreen` = the peer is.
  const [sharing, setSharing] = useState(false);
  const [remoteScreen, setRemoteScreen] = useState(false);
  const [screenMinimized, setScreenMinimized] = useState(false);
  const [screenExpanded, setScreenExpanded] = useState(false);
  const [screenReady, setScreenReady] = useState(false);
  const [screenStats, setScreenStats] = useState("");
  const [screenWidth, setScreenWidth] = useState(() => {
    const stored = parseInt(readStored("cloudix:screen-width", "760"), 10);
    return Number.isFinite(stored) ? Math.min(1600, Math.max(320, stored)) : 760;
  });
  const [screenVolume, setScreenVolume] = useState(1);
  const [diagOpen, setDiagOpen] = useState(false);
  const [diag, setDiag] = useState("");

  const handleVolumeChange = (e) => {
    const v = parseFloat(e.target.value);
    remoteVolumeRef.current = v;
    setRemoteVolume(v);
    if (remoteAudioRef.current) remoteAudioRef.current.volume = v;
  };

  const pcRef = useRef(null);
  const localStreamRef = useRef(null);
  const remoteStreamRef = useRef(new MediaStream());
  const localVideoRef = useRef(null);
  const remoteVideoRef = useRef(null);
  const remoteAudioRef = useRef(null);
  const pendingIceRef = useRef([]);
  const earlyIceRef = useRef([]);
  const unregisterRef = useRef(null);
  const startedRef = useRef(false);
  const makingOfferRef = useRef(false);
  const ignoreOfferRef = useRef(false);
  const politeRef = useRef(!isCaller);
  const closedRef = useRef(false);
  const localVideoSenderRef = useRef(null);
  const disconnectTimerRef = useRef(null);
  const screenStreamRef = useRef(null);
  const screenSendersRef = useRef([]);
  const remoteScreenStreamRef = useRef(new MediaStream());
  const screenVideoRef = useRef(null);
  const screenStageRef = useRef(null);
  const screenVolumeRef = useRef(1);
  const screenWidthRef = useRef(760);
  const screenVideoSenderRef = useRef(null);
  const screenAudioStreamRef = useRef(null);
  const screenAudioSenderRef = useRef(null);
  const sharingRef = useRef(false);
  const micSenderRef = useRef(null);
  const screenQualityRef = useRef(loadScreenQuality());
  const screenX = useMotionValue(0);
  const screenY = useMotionValue(0);
  // Track ids the peer announced as their screen (WebRTC carries no such
  // labelling, so it rides on a `screen-on` signal).
  const screenIdsRef = useRef({ mid: "", video: "", audio: "" });
  // Every remote track we have seen, so a late `screen-on` can reclassify them.
  const receivedTracksRef = useRef([]);
  const iceStatsRef = useRef({
    sent: 0,
    received: 0,
    added: 0,
    rejected: 0,
    lastError: "",
    lastCandidate: "",
  });
  // Best-known real IP of the peer, used to de-obfuscate their mDNS ICE
  // candidates. Seeded from discovery, refreshed from every incoming signal.
  const peerIpRef = useRef(target.ip || "");

  const firstMid = (pc) => {
    const mids = (pc.remoteDescription?.sdp || "").match(/^a=mid:(.+)$/m);
    return mids ? mids[1].trim() : "0";
  };

  // Implementations disagree about mDNS names and about mid naming under
  // BUNDLE, and a rejected candidate is silent. So offer several equivalent
  // forms and keep whichever the local stack accepts.
  const addRemoteCandidate = async (pc, raw) => {
    let parsed;
    try {
      parsed = JSON.parse(raw);
    } catch {
      return;
    }
    if (!parsed || !parsed.candidate) return;

    iceStatsRef.current.received += 1;

    const rewritten = rewriteMdnsCandidate(parsed, peerIpRef.current);
    const primary = [];
    if (rewritten.candidate !== parsed.candidate) primary.push(rewritten);
    primary.push(parsed);

    let accepted = 0;
    let lastErr = null;

    for (const variant of primary) {
      try {
        await pc.addIceCandidate(variant);
        accepted += 1;
      } catch (err) {
        lastErr = err;
      }
    }

    if (!accepted) {
      for (const variant of primary) {
        try {
          await pc.addIceCandidate({
            candidate: variant.candidate,
            sdpMid: firstMid(pc),
            sdpMLineIndex: 0,
          });
          accepted += 1;
          break;
        } catch (err) {
          lastErr = err;
        }
      }
    }

    if (accepted) {
      iceStatsRef.current.added += 1;
    } else {
      iceStatsRef.current.rejected += 1;
      iceStatsRef.current.lastError = String(lastErr?.message || lastErr || "unknown");
      iceStatsRef.current.lastCandidate = String(parsed.candidate).slice(0, 120);
      console.warn("addIceCandidate rejected all variants", lastErr, parsed.candidate);
    }
  };

  const drainPendingIce = async () => {
    const pc = pcRef.current;
    if (!pc || !pc.remoteDescription) return;
    const queued = pendingIceRef.current;
    pendingIceRef.current = [];
    for (const cand of queued) {
      try {
        await addRemoteCandidate(pc, cand);
      } catch (err) {
        console.warn("drainPendingIce addIceCandidate failed", err);
      }
    }
  };

  const clearCallError = () => setErrorText("");

  const isScreenTrack = (id) =>
    !!id && (id === screenIdsRef.current.video || id === screenIdsRef.current.audio);

  // Route a remote track to either the camera/voice stream or the screen-share
  // stream, moving it if a `screen-on` signal arrived after the track did.
  const routeTrack = (track) => {
    const toScreen = isScreenTrack(track.id);
    const dest = toScreen ? remoteScreenStreamRef.current : remoteStreamRef.current;
    const other = toScreen ? remoteStreamRef.current : remoteScreenStreamRef.current;
    try {
      if (other.getTracks().some((tr) => tr.id === track.id)) other.removeTrack(track);
      if (!dest.getTracks().some((tr) => tr.id === track.id)) dest.addTrack(track);
    } catch (err) {
      console.warn("routeTrack failed", err);
    }
  };

  const reclassifyTracks = () => {
    receivedTracksRef.current = receivedTracksRef.current.filter(
      (tr) => tr.readyState !== "ended"
    );
    receivedTracksRef.current.forEach(routeTrack);
  };

  const watchTrack = (track) => {
    const refresh = async () => {
      await refreshRemoteVideoUi();
      await refreshScreenUi();
    };
    track.onended = refresh;
    track.onmute = refresh;
    track.onunmute = refresh;
  };

  // `ontrack` does not fire again when a transceiver is reused, which is exactly
  // what happens when a peer stops sharing and starts again, or when the two
  // directions are toggled in turn. So instead of trusting that event, sweep the
  // receivers directly and anchor the screen to the transceiver mid the peer
  // announced — mids are stable across renegotiation, track ids are not.
  const resolveScreenTracks = () => {
    const pc = pcRef.current;
    if (!pc) return;

    pc.getReceivers().forEach((receiver) => {
      const track = receiver.track;
      if (!track) return;
      if (!receivedTracksRef.current.some((tr) => tr.id === track.id)) {
        receivedTracksRef.current.push(track);
        watchTrack(track);
      }
    });

    const mid = screenIdsRef.current.mid;
    if (mid) {
      const transceiver = pc.getTransceivers().find((tr) => tr.mid === mid);
      const track = transceiver?.receiver?.track;
      if (track) screenIdsRef.current.video = track.id;
    }

    reclassifyTracks();
  };

  const refreshScreenUi = async () => {
    const live = remoteScreenStreamRef.current
      .getVideoTracks()
      .some((tr) => tr.readyState === "live");
    setRemoteScreen(live);
    if (!live) {
      setScreenMinimized(false);
      if (screenVideoRef.current) {
        screenVideoRef.current.pause?.();
        screenVideoRef.current.srcObject = null;
      }
      return;
    }
    try {
      if (screenVideoRef.current) {
        screenVideoRef.current.srcObject = remoteScreenStreamRef.current;
        // The share's own audio lives on this element, so its volume is
        // independent of the voice-call volume.
        screenVideoRef.current.muted = false;
        screenVideoRef.current.volume = screenVolumeRef.current;
        await screenVideoRef.current.play().catch(() => {});
      }
    } catch (err) {
      console.warn("refreshScreenUi failed:", err);
    }
  };

  useEffect(() => {
    screenWidthRef.current = screenWidth;
  }, [screenWidth]);

  // Changing the profile in Settings takes effect on a live share: setParameters
  // needs no renegotiation.
  useEffect(() => {
    const onQuality = (e) => {
      const next = e.detail || loadScreenQuality();
      const prev = screenQualityRef.current;
      screenQualityRef.current = next;
      applyScreenEncoding();
      if (prev.audioSource !== next.audioSource) switchScreenAudio(next.audioSource);
    };
    const onMic = (e) => switchMicrophone(e.detail ?? loadMicDevice());

    window.addEventListener(SCREEN_QUALITY_EVENT, onQuality);
    window.addEventListener(MIC_DEVICE_EVENT, onMic);
    return () => {
      window.removeEventListener(SCREEN_QUALITY_EVENT, onQuality);
      window.removeEventListener(MIC_DEVICE_EVENT, onMic);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [muted]);

  // Real resolution / framerate / bitrate of the share, so a degraded picture is
  // visible as numbers instead of guesswork.
  useEffect(() => {
    if (!sharing && !remoteScreen) {
      setScreenStats("");
      return;
    }
    let alive = true;
    let prevBytes = 0;
    let prevAt = 0;

    const sample = async () => {
      const pc = pcRef.current;
      if (!pc) return;
      try {
        const stats = await pc.getStats();
        const sources = new Map();
        let report = null;

        stats.forEach((r) => {
          if (r.type === "media-source") sources.set(r.id, r);
        });

        stats.forEach((r) => {
          if (sharing && r.type === "outbound-rtp" && r.kind === "video") {
            const src = r.mediaSourceId ? sources.get(r.mediaSourceId) : null;
            const trackId = src?.trackIdentifier;
            if (!trackId || trackId === screenVideoSenderRef.current?.track?.id) report = r;
          }
          if (!sharing && r.type === "inbound-rtp" && r.kind === "video") {
            if (r.trackIdentifier === screenIdsRef.current.video) report = r;
          }
        });

        if (!report) return;

        const bytes = report.bytesSent ?? report.bytesReceived ?? 0;
        const now = report.timestamp || Date.now();
        let bps = 0;
        if (prevAt && now > prevAt) {
          bps = ((bytes - prevBytes) * 8000) / (now - prevAt);
        }
        prevBytes = bytes;
        prevAt = now;

        const w = report.frameWidth;
        const h = report.frameHeight;
        const fps = report.framesPerSecond;
        const parts = [];
        if (w && h) parts.push(`${w}×${h}`);
        if (fps != null) parts.push(`${Math.round(fps)} fps`);
        if (bps > 0) parts.push(fmtBitrate(bps));
        if (alive) setScreenStats(parts.join(" · "));
      } catch {
        /* stats are best-effort */
      }
    };

    sample();
    const timer = setInterval(sample, 1500);
    return () => {
      alive = false;
      clearInterval(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sharing, remoteScreen]);

  useEffect(() => {
    if (!remoteScreen) {
      setScreenReady(false);
      setScreenExpanded(false);
    }
  }, [remoteScreen]);

  const handleScreenVolumeChange = (e) => {
    const v = parseFloat(e.target.value);
    screenVolumeRef.current = v;
    setScreenVolume(v);
    if (screenVideoRef.current) screenVideoRef.current.volume = v;
  };

  const attachRemoteStream = async () => {
    try {
      if (remoteAudioRef.current) {
        remoteAudioRef.current.srcObject = remoteStreamRef.current;
        remoteAudioRef.current.muted = false;
        remoteAudioRef.current.volume = remoteVolumeRef.current;
        await remoteAudioRef.current.play().catch(() => {});
      }
    } catch (err) {
      console.warn("attachRemoteStream (audio) failed:", err);
    }
  };

  const refreshRemoteVideoUi = async () => {
    const liveVideoTracks = remoteStreamRef.current
      .getVideoTracks()
      .filter((tr) => tr.readyState === "live" && tr.enabled !== false && !tr.muted);

    const hasVideo = liveVideoTracks.length > 0;
    setRemoteHasVideo(hasVideo);
    if (!hasVideo) setRemoteZoomed(false);

    try {
      if (!hasVideo) {
        if (remoteVideoRef.current) {
          remoteVideoRef.current.pause?.();
          remoteVideoRef.current.srcObject = null;
        }
      } else if (remoteVideoRef.current) {
        remoteVideoRef.current.srcObject = remoteStreamRef.current;
        remoteVideoRef.current.autoplay = true;
        remoteVideoRef.current.playsInline = true;
        await remoteVideoRef.current.play().catch(() => {});
      }
    } catch (err) {
      console.warn("refreshRemoteVideoUi failed:", err);
    }
  };

  const stopAllMedia = () => {
    try {
      localStreamRef.current?.getTracks().forEach((tr) => tr.stop());
    } catch {}
    try {
      remoteStreamRef.current?.getTracks().forEach((tr) => tr.stop());
    } catch {}
    try {
      screenStreamRef.current?.getTracks().forEach((tr) => tr.stop());
    } catch {}
    try {
      screenAudioStreamRef.current?.getTracks().forEach((tr) => tr.stop());
    } catch {}
    try {
      remoteScreenStreamRef.current?.getTracks().forEach((tr) => tr.stop());
    } catch {}
  };

  const resetCallUiState = () => {
    setPhase(isCaller ? "calling" : "ringing");
    setMuted(false);
    setRemoteHasVideo(false);
    setRemoteZoomed(false);
    sharingRef.current = false;
    setSharing(false);
    setRemoteScreen(false);
    setScreenMinimized(false);
    setScreenExpanded(false);
    setScreenReady(false);
    setScreenStats("");
    setDiagOpen(false);
    screenIdsRef.current = { mid: "", video: "", audio: "" };
    screenSendersRef.current = [];
    receivedTracksRef.current = [];
    setSeconds(0);
    setErrorText("");
    pendingIceRef.current = [];
    earlyIceRef.current = [];
    makingOfferRef.current = false;
    ignoreOfferRef.current = false;
    startedRef.current = false;
  };

  const cleanupCall = useCallback(
    (notify = false, kind = "end") => {
      if (closedRef.current) return;
      closedRef.current = true;

      if (disconnectTimerRef.current) {
        clearTimeout(disconnectTimerRef.current);
        disconnectTimerRef.current = null;
      }

      try {
        if (notify) {
          WailsApp.SendSignal(target.peerId, callId, kind, "", video).catch(() => {});
        }
      } catch {}

      try {
        unregisterRef.current?.();
      } catch {}

      try {
        if (localVideoRef.current) localVideoRef.current.srcObject = null;
        if (remoteVideoRef.current) remoteVideoRef.current.srcObject = null;
        if (remoteAudioRef.current) remoteAudioRef.current.srcObject = null;
        if (screenVideoRef.current) screenVideoRef.current.srcObject = null;
        if (document.fullscreenElement) document.exitFullscreen?.().catch(() => {});
      } catch {}

      stopAllMedia();

      try {
        pcRef.current?.close();
      } catch {}

      pcRef.current = null;
      localStreamRef.current = null;
      remoteStreamRef.current = new MediaStream();
      remoteScreenStreamRef.current = new MediaStream();
      screenStreamRef.current = null;
      localVideoSenderRef.current = null;

      resetCallUiState();

      try {
        onClose();
      } catch (err) {
        console.error("cleanupCall onClose failed:", err);
      }
    },
    [callId, onClose, target.peerId, video]
  );

  const createPeerConnection = () => {
    if (pcRef.current) return pcRef.current;

    let pc;
    try {
      pc = new RTCPeerConnection(buildRtcConfig());
    } catch (err) {
      // A malformed TURN entry used to take the whole call down with it.
      console.error("ICE configuration rejected, continuing without TURN:", err);
      setErrorText(t.call.errIceConfig);
      pc = new RTCPeerConnection({ iceServers: [{ urls: DEFAULT_STUN }] });
    }
    pcRef.current = pc;

    if (earlyIceRef.current.length) {
      pendingIceRef.current.push(...earlyIceRef.current);
      earlyIceRef.current = [];
    }

    pc.onicecandidate = (e) => {
      if (e.candidate) {
        iceStatsRef.current.sent += 1;
        WailsApp.SendSignal(
          target.peerId,
          callId,
          "ice",
          JSON.stringify(e.candidate),
          video
        ).catch((err) => {
          console.error("SendSignal ice failed:", err);
        });
      }
    };

    pc.ontrack = async (e) => {
      const track = e.track;

      // We keep our own streams rather than adopting e.streams[0], because a
      // track has to be routable between the camera surface and the
      // screen-share surface as `screen-on`/`screen-off` arrive.
      if (!receivedTracksRef.current.some((tr) => tr.id === track.id)) {
        receivedTracksRef.current.push(track);
      }
      routeTrack(track);

      watchTrack(track);

      await attachRemoteStream();
      await refreshRemoteVideoUi();
      await refreshScreenUi();
    };

    const markConnected = () => {
      if (disconnectTimerRef.current) {
        clearTimeout(disconnectTimerRef.current);
        disconnectTimerRef.current = null;
      }
      setPhase("connected");
      clearCallError();
    };

    const enterDisconnectedGrace = () => {
      setErrorText(t.call.reconnecting);
      if (!disconnectTimerRef.current) {
        disconnectTimerRef.current = setTimeout(() => {
          disconnectTimerRef.current = null;
          const stillBad =
            pcRef.current &&
            pcRef.current.connectionState !== "connected" &&
            pcRef.current.iceConnectionState !== "connected" &&
            pcRef.current.iceConnectionState !== "completed";
          if (stillBad) cleanupCall(false);
        }, 8000);
      }
    };

    pc.onconnectionstatechange = () => {
      const st = pc.connectionState;
      if (st === "connected") markConnected();
      if (st === "failed") cleanupCall(false);
      if (st === "disconnected") enterDisconnectedGrace();
    };

    // Fallback for WebViews where RTCPeerConnection.connectionState /
    // onconnectionstatechange isn't implemented (older WebKit): drive the call
    // phase off iceConnectionState instead, otherwise the call connects but the
    // UI stays stuck on "calling" forever.
    pc.oniceconnectionstatechange = () => {
      const ice = pc.iceConnectionState;
      if (ice === "connected" || ice === "completed") {
        markConnected();
      } else if (ice === "failed") {
        cleanupCall(false);
      } else if (ice === "disconnected" && !pc.connectionState) {
        enterDisconnectedGrace();
      }
    };

    return pc;
  };

  const startLocalMedia = async () => {
    if (localStreamRef.current) return localStreamRef.current;
    if (!navigator.mediaDevices?.getUserMedia) {
      throw new Error("getUserMedia is unavailable");
    }

    try {
      const micId = loadMicDevice();
      const audioConstraint = micId ? { deviceId: { exact: micId } } : true;
      let stream;
      try {
        stream = await navigator.mediaDevices.getUserMedia({
          audio: audioConstraint,
          video,
        });
      } catch (err) {
        // A remembered device can disappear (unplugged headset); fall back to
        // the system default rather than failing the whole call.
        if (!micId) throw err;
        console.warn("preferred mic unavailable, using default", err);
        stream = await navigator.mediaDevices.getUserMedia({ audio: true, video });
      }
      localStreamRef.current = stream;

      if (video && localVideoRef.current) {
        localVideoRef.current.srcObject = stream;
        localVideoRef.current.muted = true;
        localVideoRef.current.play().catch(() => {});
      }

      return stream;
    } catch (err) {
      setErrorText(t.call.errMedia);
      throw err;
    }
  };

  const addLocalTracksToPeer = async (pc, stream) => {
    const existing = new Set(pc.getSenders().map((s) => s.track?.id).filter(Boolean));
    stream.getTracks().forEach((track) => {
      if (!existing.has(track.id)) {
        const sender = pc.addTrack(track, stream);
        if (track.kind === "video") localVideoSenderRef.current = sender;
        if (track.kind === "audio") micSenderRef.current = sender;
      }
    });
  };

  // replaceTrack swaps the encoder input in place, so switching microphones
  // mid-call needs no renegotiation and the peer hears no gap.
  const switchMicrophone = async (deviceId) => {
    const sender = micSenderRef.current;
    if (!sender || closedRef.current) return;

    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: deviceId ? { deviceId: { exact: deviceId } } : true,
      });
    } catch (err) {
      console.warn("microphone switch failed", err);
      return;
    }

    const track = stream.getAudioTracks()[0];
    if (!track) return;
    // Carry the current mute state over to the new input.
    track.enabled = !muted;

    try {
      await sender.replaceTrack(track);
    } catch (err) {
      console.warn("replaceTrack failed", err);
      stream.getTracks().forEach((tr) => tr.stop());
      return;
    }

    const old = localStreamRef.current;
    if (old) {
      old.getAudioTracks().forEach((tr) => {
        try {
          old.removeTrack(tr);
          tr.stop();
        } catch {}
      });
      old.addTrack(track);
    } else {
      localStreamRef.current = stream;
    }
  };

  // Renegotiation is needed whenever tracks are added or removed mid-call
  // (screen share on/off). The peer answers with `renegotiate-answer`.
  const renegotiate = async () => {
    const pc = pcRef.current;
    if (!pc || closedRef.current) return;
    try {
      makingOfferRef.current = true;
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      await WailsApp.SendSignal(
        target.peerId,
        callId,
        "renegotiate-offer",
        JSON.stringify(pc.localDescription),
        true
      );
    } catch (err) {
      console.error("renegotiate failed:", err);
    } finally {
      makingOfferRef.current = false;
    }
  };

  // Sender parameters do not survive a renegotiation, so this is applied both
  // when the share starts and again after every answer. Losing it was the main
  // reason the picture turned blocky: the encoder fell back to its default
  // ceiling, which is far below what a LAN can carry.
  const applyScreenEncoding = async () => {
    const sender = screenVideoSenderRef.current;
    if (!sender) return;
    const quality = screenQualityRef.current;
    const profile = encodingForQuality(quality);

    // getDisplayMedia largely ignores resolution constraints on macOS — it hands
    // back the native display size — so the resolution setting is enforced
    // encoder-side instead, which both engines honour.
    let scale = 1;
    try {
      const settings = sender.track?.getSettings?.() || {};
      if (settings.height && settings.height > quality.height) {
        scale = settings.height / quality.height;
      }
    } catch {}

    try {
      const params = sender.getParameters();
      params.degradationPreference = profile.degradationPreference;
      if (!params.encodings || params.encodings.length === 0) {
        params.encodings = [{}];
      }
      params.encodings[0] = {
        ...params.encodings[0],
        ...profile.encoding,
        scaleResolutionDownBy: scale,
      };
      await sender.setParameters(params);
      console.log("screen encoding applied", {
        ...profile.encoding,
        scaleResolutionDownBy: scale,
        degradationPreference: profile.degradationPreference,
        contentHint: profile.contentHint,
      });
    } catch (err) {
      console.warn("screen encoding params not applied", err);
    }

    try {
      if (sender.track) sender.track.contentHint = profile.contentHint;
    } catch {}

    // Best effort: ask the capture itself to slow down too, so we are not
    // encoding frames we intend to throw away.
    try {
      await sender.track?.applyConstraints?.({
        frameRate: { ideal: quality.fps, max: quality.fps },
      });
    } catch {}
  };

  // Re-announced after every negotiation: the transceiver mid only exists once
  // the exchange has settled, and it is the only identifier that survives a
  // transceiver being reused for a second share.
  const announceScreen = async () => {
    if (!sharingRef.current || closedRef.current) return;
    const pc = pcRef.current;
    const sender = screenVideoSenderRef.current;
    if (!pc || !sender) return;

    const transceiver = pc.getTransceivers().find((tr) => tr.sender === sender);
    try {
      await WailsApp.SendSignal(
        target.peerId,
        callId,
        "screen-on",
        JSON.stringify({
          mid: transceiver?.mid || "",
          video: sender.track?.id || "",
          audio: screenAudioSenderRef.current?.track?.id || "",
        }),
        true
      );
    } catch (err) {
      console.warn("announceScreen failed", err);
    }
  };

  const startScreenShare = async () => {
    if (sharing || closedRef.current) return;
    const pc = pcRef.current;
    if (!pc) return;

    const quality = loadScreenQuality();
    screenQualityRef.current = quality;

    try {
      const stream = await navigator.mediaDevices.getDisplayMedia({
        video: {
          width: { ideal: Math.round((quality.height * 16) / 9) },
          height: { ideal: quality.height, max: quality.height },
          frameRate: { ideal: quality.fps, max: quality.fps },
        },
        audio: true,
      });
      screenStreamRef.current = stream;

      const videoTrack = stream.getVideoTracks()[0];
      let audioTrack = stream.getAudioTracks()[0];
      const senders = [];

      // WebKit cannot hand over system audio via getDisplayMedia, so on macOS
      // the user routes output into a loopback device (BlackHole and friends)
      // and picks it here; we capture it like any other input.
      if (!audioTrack && quality.audioSource && quality.audioSource !== "none") {
        try {
          const extra = await navigator.mediaDevices.getUserMedia({
            audio: {
              deviceId: { exact: quality.audioSource },
              echoCancellation: false,
              noiseSuppression: false,
              autoGainControl: false,
            },
          });
          audioTrack = extra.getAudioTracks()[0];
          if (audioTrack) {
            screenAudioStreamRef.current = extra;
            stream.addTrack(audioTrack);
          }
        } catch (err) {
          console.warn("screen audio device capture failed", err);
        }
      }

      if (videoTrack) {
        const sender = pc.addTrack(videoTrack, stream);
        senders.push(sender);
        screenVideoSenderRef.current = sender;
        await applyScreenEncoding();
        // The OS "stop sharing" bar ends the track directly.
        videoTrack.onended = () => {
          stopScreenShare();
        };
      }
      if (audioTrack) {
        const audioSender = pc.addTrack(audioTrack, stream);
        senders.push(audioSender);
        screenAudioSenderRef.current = audioSender;
      }

      screenSendersRef.current = senders;
      sharingRef.current = true;
      setSharing(true);

      // Announced twice on purpose: once now so a receiver that gets an
      // ontrack event can bind immediately, and again after the exchange
      // settles, when the mid exists.
      await announceScreen();
      await renegotiate();
      await announceScreen();
    } catch (err) {
      // A user cancelling the picker is not an error worth showing.
      if (err?.name !== "NotAllowedError" && err?.name !== "AbortError") {
        console.error("startScreenShare failed:", err);
        setErrorText(t.call.errShare);
      }
      screenStreamRef.current?.getTracks().forEach((tr) => tr.stop());
      screenStreamRef.current = null;
      setSharing(false);
    }
  };

  // Changing the share's audio source mid-share: replaceTrack when a sender
  // already exists (no renegotiation), otherwise add one and renegotiate.
  const switchScreenAudio = async (deviceId) => {
    if (!sharingRef.current || closedRef.current) return;
    const pc = pcRef.current;
    if (!pc) return;

    const stopPrevious = () => {
      try {
        screenAudioStreamRef.current?.getTracks().forEach((tr) => tr.stop());
      } catch {}
      screenAudioStreamRef.current = null;
    };

    let track = null;
    if (deviceId && deviceId !== "none") {
      try {
        const stream = await navigator.mediaDevices.getUserMedia({
          audio: {
            deviceId: { exact: deviceId },
            echoCancellation: false,
            noiseSuppression: false,
            autoGainControl: false,
          },
        });
        track = stream.getAudioTracks()[0] || null;
        if (track) {
          stopPrevious();
          screenAudioStreamRef.current = stream;
        } else {
          stream.getTracks().forEach((tr) => tr.stop());
        }
      } catch (err) {
        console.warn("screen audio switch failed", err);
        return;
      }
    } else {
      stopPrevious();
    }

    const sender = screenAudioSenderRef.current;
    if (sender) {
      try {
        await sender.replaceTrack(track);
      } catch (err) {
        console.warn("screen audio replaceTrack failed", err);
      }
      return;
    }

    if (!track) return;

    const added = pc.addTrack(track, screenStreamRef.current || new MediaStream());
    screenAudioSenderRef.current = added;
    screenSendersRef.current = [...screenSendersRef.current, added];
    await renegotiate();
    await announceScreen();
  };

  const stopScreenShare = async () => {
    const pc = pcRef.current;
    const senders = screenSendersRef.current;
    screenSendersRef.current = [];
    screenVideoSenderRef.current = null;
    screenAudioSenderRef.current = null;
    sharingRef.current = false;

    senders.forEach((sender) => {
      try {
        pc?.removeTrack(sender);
      } catch (err) {
        console.warn("removeTrack failed", err);
      }
    });
    try {
      screenStreamRef.current?.getTracks().forEach((tr) => tr.stop());
    } catch {}
    try {
      screenAudioStreamRef.current?.getTracks().forEach((tr) => tr.stop());
    } catch {}
    screenStreamRef.current = null;
    screenAudioStreamRef.current = null;
    setSharing(false);

    if (closedRef.current) return;
    try {
      await WailsApp.SendSignal(target.peerId, callId, "screen-off", "", false);
    } catch {}
    await renegotiate();
  };

  const toggleScreenShare = () => {
    if (sharing) stopScreenShare();
    else startScreenShare();
  };

  // Expand fills the app window instead of calling requestFullscreen: element
  // fullscreen is a no-op in WKWebView and blacked out the whole window in
  // WebView2, so this behaves the same on both platforms.
  const toggleScreenExpanded = () => {
    setScreenExpanded((v) => {
      const next = !v;
      if (next) {
        screenX.set(0);
        screenY.set(0);
        setScreenMinimized(false);
      }
      return next;
    });
  };

  const startScreenResize = (e) => {
    e.preventDefault();
    e.stopPropagation();
    const startX = e.clientX;
    const startW = screenWidthRef.current;

    const onMove = (ev) => {
      // Panel is centre-anchored, so it grows from both edges at once.
      const next = Math.max(320, Math.min(1600, startW + (ev.clientX - startX) * 2));
      screenWidthRef.current = next;
      setScreenWidth(next);
    };
    const onUp = () => {
      document.removeEventListener("pointermove", onMove);
      document.removeEventListener("pointerup", onUp);
      try {
        localStorage.setItem("cloudix:screen-width", String(screenWidthRef.current));
      } catch {}
    };
    document.addEventListener("pointermove", onMove);
    document.addEventListener("pointerup", onUp);
  };

  const handleOfferLike = async (pc, payload) => {
    const offerCollision = pc.signalingState !== "stable";
    ignoreOfferRef.current = !politeRef.current && offerCollision;
    if (ignoreOfferRef.current) return;

    await pc.setRemoteDescription(rewriteSdpMdns(JSON.parse(payload.data), peerIpRef.current));
    await drainPendingIce();

    const answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);

    if (payload.kind === "offer") {
      await WailsApp.SendSignal(
        target.peerId,
        callId,
        "answer",
        JSON.stringify(answer),
        payload.video === true
      );
    } else {
      await WailsApp.SendSignal(
        target.peerId,
        callId,
        "renegotiate-answer",
        JSON.stringify(answer),
        true
      );
    }
  };

  const handleSignal = async (payload) => {
    if (payload.callId !== callId || closedRef.current) return;

    // The backend stamps every inbound signal with the peer's real address.
    if (payload.peerIp) peerIpRef.current = payload.peerIp;

    // FIX: асинхронная ошибка отправки сигнала (SendSignal теперь
    // выполняет реальную сетевую отправку в горутине на Go-стороне и
    // возвращается немедленно; если отправка всё же не удалась —
    // ошибка приходит сюда как отдельное событие с kind "send_error",
    // а не как rejected promise). Раньше при недостижимости пира кнопка
    // "Принять" выглядела так, будто она "ничего не делает".
    if (payload.kind === "send_error") {
      console.error("Signal send failed:", payload);
      setErrorText(t.call.errSignalSend);
      return;
    }

    if (payload.kind === "reject" || payload.kind === "end") {
      cleanupCall(false);
      return;
    }

    // The peer tells us which inbound track ids carry their screen; the tracks
    // themselves may arrive before or after this, so always reclassify.
    if (payload.kind === "screen-on") {
      try {
        const ids = JSON.parse(payload.data || "{}");
        screenIdsRef.current = {
          mid: ids.mid || "",
          video: ids.video || "",
          audio: ids.audio || "",
        };
      } catch {
        screenIdsRef.current = { mid: "", video: "", audio: "" };
      }
      resolveScreenTracks();
      await refreshRemoteVideoUi();
      await refreshScreenUi();
      return;
    }

    if (payload.kind === "screen-off") {
      const ids = screenIdsRef.current;
      screenIdsRef.current = { mid: "", video: "", audio: "" };
      // Forget the screen tracks entirely. The sender's removeTrack leaves them
      // live-but-muted here, so reclassifying them would move a black frame
      // into the camera surface of the call card.
      receivedTracksRef.current = receivedTracksRef.current.filter(
        (tr) => tr.id !== ids.video && tr.id !== ids.audio
      );
      try {
        remoteScreenStreamRef.current
          .getTracks()
          .forEach((tr) => remoteScreenStreamRef.current.removeTrack(tr));
      } catch {}
      reclassifyTracks();
      await refreshRemoteVideoUi();
      await refreshScreenUi();
      return;
    }

    const pc = pcRef.current;

    if (!pc) {
      if (payload.kind === "ice") {
        earlyIceRef.current.push(payload.data);
      }
      return;
    }

    try {
      if (payload.kind === "answer" || payload.kind === "renegotiate-answer") {
        await pc.setRemoteDescription(rewriteSdpMdns(JSON.parse(payload.data), peerIpRef.current));
        await drainPendingIce();

        await attachRemoteStream();
        await refreshRemoteVideoUi();
        resolveScreenTracks();
        await refreshScreenUi();
        await applyScreenEncoding();
        await announceScreen();
      } else if (payload.kind === "ice") {
        if (pc.remoteDescription) {
          try {
            await addRemoteCandidate(pc, payload.data);
          } catch (err) {
            console.warn("addIceCandidate failed", err);
          }
        } else {
          pendingIceRef.current.push(payload.data);
        }
      } else if (payload.kind === "offer" || payload.kind === "renegotiate-offer") {
        await handleOfferLike(pc, payload);
        await attachRemoteStream();
        await refreshRemoteVideoUi();
        resolveScreenTracks();
        await refreshScreenUi();
        await applyScreenEncoding();
        await announceScreen();
      }
    } catch (err) {
      console.error("Signal handler failed:", err, payload);
      setErrorText(t.call.errSignal);
    }
  };

  const startConnection = async () => {
    if (startedRef.current) return;
    startedRef.current = true;
    clearCallError();

    try {
      const pc = createPeerConnection();
      const stream = await startLocalMedia();
      await addLocalTracksToPeer(pc, stream);

      if (!isCaller && incomingOffer) {
        await pc.setRemoteDescription(rewriteSdpMdns(JSON.parse(incomingOffer), peerIpRef.current));
        await drainPendingIce();
        const answer = await pc.createAnswer();
        await pc.setLocalDescription(answer);
        await WailsApp.SendSignal(target.peerId, callId, "answer", JSON.stringify(answer), video);
        await drainPendingIce();
        setPhase("calling");
        return;
      }

      if (isCaller) {
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        await WailsApp.SendSignal(target.peerId, callId, "offer", JSON.stringify(offer), video);
        setPhase("calling");
      } else {
        setPhase("calling");
      }

      await attachRemoteStream();
      await refreshRemoteVideoUi();
    } catch (err) {
      // FIX: раньше ошибка (например, отклонённый промис от SendSignal,
      // если Go-метод синхронно вернул "peer not found") тихо логировалась
      // в консоль и НИЧЕГО не показывала пользователю — кнопка "Принять"
      // выглядела нерабочей, хотя на самом деле клик обрабатывался и падал
      // молча. Теперь ошибка отображается в call-status.
      console.error("startConnection failed:", err);
      startedRef.current = false;
      setErrorText(t.call.errConnect);
    }
  };

  useEffect(() => {
    closedRef.current = false;
    unregisterRef.current = registerSignalHandler(handleSignal);
    if (isCaller) startConnection();

    return () => {
      try {
        unregisterRef.current?.();
      } catch {}
      stopAllMedia();
      try {
        pcRef.current?.close();
      } catch {}
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [callId]);

  useEffect(() => {
    if (phase !== "connected") return;
    const timer = setInterval(() => setSeconds((s) => s + 1), 1000);
    return () => clearInterval(timer);
  }, [phase]);

  // Without this a call that never negotiates just sits on "calling" forever.
  useEffect(() => {
    if (phase !== "calling") return;
    const timer = setTimeout(() => {
      if (pcRef.current && pcRef.current.connectionState !== "connected") {
        setErrorText(t.call.errTimeout);
      }
    }, 30000);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [phase]);

  // Live ICE diagnostics. Cross-platform LAN calls fail in ways that are
  // invisible from the UI ("calling" forever), so surface the real states and
  // the candidate pair actually chosen.
  useEffect(() => {
    if (!diagOpen) return;
    let alive = true;

    const sample = async () => {
      const pc = pcRef.current;
      if (!pc) {
        if (alive) setDiag("no peer connection");
        return;
      }
      const lines = [
        `signaling:  ${pc.signalingState}`,
        `ice-gather: ${pc.iceGatheringState}`,
        `ice-conn:   ${pc.iceConnectionState}`,
        `conn:       ${pc.connectionState ?? "(unsupported)"}`,
        `peer-ip:    ${peerIpRef.current || "(unknown)"}`,
        `cand sent:  ${iceStatsRef.current.sent}`,
        `cand recv:  ${iceStatsRef.current.received}` +
          ` added:${iceStatsRef.current.added} rejected:${iceStatsRef.current.rejected}`,
        `cand queued: ${pendingIceRef.current.length + earlyIceRef.current.length}`,
      ];
      if (iceStatsRef.current.rejected) {
        lines.push(`reject err: ${iceStatsRef.current.lastError}`);
        lines.push(`reject cand: ${iceStatsRef.current.lastCandidate}`);
      }
      try {
        const stats = await pc.getStats();
        const local = {};
        const remote = {};
        const byId = new Map();
        let pair = null;
        stats.forEach((r) => {
          byId.set(r.id, r);
          if (r.type === "local-candidate") local[r.candidateType] = (local[r.candidateType] || 0) + 1;
          if (r.type === "remote-candidate") remote[r.candidateType] = (remote[r.candidateType] || 0) + 1;
          if (r.type === "candidate-pair" && (r.selected || r.state === "succeeded")) pair = r;
        });
        const fmtCount = (o) =>
          Object.keys(o).length
            ? Object.entries(o).map(([k, v]) => `${k}:${v}`).join(" ")
            : "none";
        lines.push(`local-cand:  ${fmtCount(local)}`);
        lines.push(`remote-cand: ${fmtCount(remote)}`);
        if (pair) {
          const lc = byId.get(pair.localCandidateId);
          const rc = byId.get(pair.remoteCandidateId);
          lines.push(
            `pair: ${lc?.candidateType || "?"} ${lc?.address || "?"}:${lc?.port || "?"}` +
              ` -> ${rc?.candidateType || "?"} ${rc?.address || "?"}:${rc?.port || "?"}`
          );
        } else {
          lines.push("pair: none succeeded yet");
        }
      } catch (err) {
        lines.push(`stats error: ${err?.message || err}`);
      }
      if (alive) setDiag(lines.join("\n"));
    };

    sample();
    const timer = setInterval(sample, 1000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [diagOpen, phase]);

  const acceptCall = async () => {
    clearCallError();
    await startConnection();
    await attachRemoteStream();
    await refreshRemoteVideoUi();
  };

  const declineCall = () => {
    cleanupCall(true, "reject");
  };

  const toggleMute = () => {
    localStreamRef.current?.getAudioTracks().forEach((tr) => {
      tr.enabled = muted;
    });
    setMuted((m) => !m);
  };

  const endCall = () => {
    cleanupCall(true, "end");
  };

  const fmt = (s) =>
    `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;

  const showRemoteVideoLayer = video || remoteHasVideo;
  const showSelfVideoLayer = video;

  return (
    // The overlay is a pass-through layer (pointer-events: none in CSS) so the
    // messenger stays usable while a call is up; only the card is interactive
    // and it can be dragged anywhere in the window by its handle.
    <div className="call-overlay">
      <motion.div
        className="call-card glass-strong"
        drag
        dragMomentum={false}
        dragElastic={0.04}
        dragListener={false}
        dragControls={dragControls}
        dragConstraints={{ left: -420, right: 420, top: -260, bottom: 260 }}
        initial={{ scale: 0.9, opacity: 0, y: 12 }}
        animate={{ scale: 1, opacity: 1, y: 0 }}
        exit={{ scale: 0.92, opacity: 0, y: 8 }}
        transition={{ type: "spring", stiffness: 320, damping: 28 }}
      >
        <div
          className="call-drag"
          title={t.call.dragHint}
          onPointerDown={(e) => dragControls.start(e)}
        />

        <audio ref={remoteAudioRef} autoPlay playsInline />

        {showRemoteVideoLayer && (
          <video
            ref={remoteVideoRef}
            autoPlay
            playsInline
            className={"call-remote-video " + (remoteZoomed ? "zoomed" : "")}
            onDoubleClick={() => setRemoteZoomed((z) => !z)}
            title={t.call.zoomHint}
          />
        )}

        {showSelfVideoLayer && (
          <video ref={localVideoRef} autoPlay playsInline muted className="call-self-video" />
        )}

        <div className="call-body">
          <Avatar name={target.title || target.name} avatar={target.avatar} size="lg" />
          <div className="call-name">{target.title || target.name || target.peerId}</div>
          <div className="call-status">
            {errorText
              ? errorText
              : phase === "ringing"
                ? t.call.incoming
                : phase === "calling"
                  ? t.call.calling
                  : fmt(seconds)}
          </div>

          {phase === "ringing" ? (
            <div className="call-actions">
              <button type="button" className="call-btn accept" onClick={acceptCall}>
                ✓
              </button>
              <button type="button" className="call-btn end" onClick={declineCall}>
                ✕
              </button>
            </div>
          ) : (
            <>
              <div className="call-actions">
                <button type="button" className="call-btn" onClick={toggleMute}>
                  {muted ? "🔇" : "🎙"}
                </button>
                {can("screenShareSend") && (
                  <button
                    type="button"
                    className={"call-btn " + (sharing ? "sharing" : "")}
                    title={sharing ? t.call.shareStop : t.call.share}
                    aria-label={sharing ? t.call.shareStop : t.call.share}
                    onClick={toggleScreenShare}
                  >
                    🖥
                  </button>
                )}
                <button type="button" className="call-btn end" onClick={endCall}>
                  ✕
                </button>
              </div>
              <div className="call-volume">
                <span>🔊</span>
                <input
                  type="range"
                  min="0"
                  max="1"
                  step="0.05"
                  value={remoteVolume}
                  onChange={handleVolumeChange}
                />
              </div>
              <div className="call-labels">
                <span>{t.call.muteShort}</span>
                <span>{t.call.shareShort}</span>
                <span>{t.call.endShort}</span>
              </div>
              {sharing && (
                <div className="call-sharing-note">
                  <span className="screen-live">live</span>
                  {t.call.sharing}
                  {screenStats && <span className="call-share-stats">{screenStats}</span>}
                </div>
              )}
              <div className="call-diag">
                <button
                  type="button"
                  className="call-diag-toggle"
                  onClick={() => setDiagOpen((v) => !v)}
                >
                  {diagOpen ? "▾ " : "▸ "}
                  {t.call.details}
                </button>
                {diagOpen && (
                  <>
                    <div className="call-diag-body">{diag || "…"}</div>
                    <button
                      type="button"
                      className="call-diag-toggle"
                      style={{ marginTop: 6 }}
                      onClick={() => navigator.clipboard?.writeText(diag).catch(() => {})}
                    >
                      ⧉ {t.call.copyDetails}
                    </button>
                  </>
                )}
              </div>
            </>
          )}
        </div>
      </motion.div>

      <AnimatePresence>
        {remoteScreen && (
          <div className="screen-layer">
            <motion.div
              className={
                "screen-panel glass-strong" + (screenExpanded ? " expanded" : "")
              }
              style={{
                x: screenX,
                y: screenY,
                width: screenExpanded ? undefined : screenWidth,
              }}
              drag={!screenExpanded}
              dragMomentum={false}
              dragElastic={0.04}
              dragListener={false}
              dragControls={screenDragControls}
              dragConstraints={{ left: -460, right: 460, top: -300, bottom: 300 }}
              initial={{ opacity: 0, scale: 0.94 }}
              animate={{ opacity: 1, scale: 1 }}
              exit={{ opacity: 0, scale: 0.94 }}
              transition={{ type: "spring", stiffness: 300, damping: 28 }}
            >
              <div
                className="screen-head"
                title={t.call.dragHint}
                onPointerDown={(e) => !screenExpanded && screenDragControls.start(e)}
                onDoubleClick={toggleScreenExpanded}
              >
                <span className="screen-live">live</span>
                <span className="screen-title">{t.call.screenOf}</span>
                {screenStats && <span className="screen-stats">{screenStats}</span>}
                <div className="screen-head-actions">
                  <button
                    type="button"
                    className="screen-btn"
                    title={screenMinimized ? t.call.expand : t.call.minimize}
                    aria-label={screenMinimized ? t.call.expand : t.call.minimize}
                    onClick={() => setScreenMinimized((v) => !v)}
                  >
                    {screenMinimized ? "▣" : "—"}
                  </button>
                  <button
                    type="button"
                    className="screen-btn"
                    title={screenExpanded ? t.call.exitFullscreen : t.call.fullscreen}
                    aria-label={screenExpanded ? t.call.exitFullscreen : t.call.fullscreen}
                    onClick={toggleScreenExpanded}
                  >
                    {screenExpanded ? "⤡" : "⛶"}
                  </button>
                </div>
              </div>

              {/* Kept mounted while minimized so the video element never loses
                  its stream (re-attaching costs a black frame + a reflow). */}
              <div
                className="screen-stage"
                ref={screenStageRef}
                style={screenMinimized ? { display: "none" } : undefined}
              >
                <video
                  ref={screenVideoRef}
                  autoPlay
                  playsInline
                  onDoubleClick={toggleScreenExpanded}
                  onLoadedMetadata={(e) =>
                    setScreenReady(e.currentTarget.videoWidth > 0)
                  }
                  onResize={(e) => setScreenReady(e.currentTarget.videoWidth > 0)}
                  onEmptied={() => setScreenReady(false)}
                />
                {!screenReady && (
                  <div className="screen-waiting">
                    <span className="screen-waiting-spinner" />
                    <span>{t.call.waiting}</span>
                  </div>
                )}
              </div>

              {!screenMinimized && (
                <div className="screen-volume" title={t.call.screenVolume}>
                  <span>🔈</span>
                  <input
                    type="range"
                    min="0"
                    max="1"
                    step="0.05"
                    value={screenVolume}
                    onChange={handleScreenVolumeChange}
                  />
                  <span className="screen-volume-value">
                    {Math.round(screenVolume * 100)}%
                  </span>
                </div>
              )}

              {!screenExpanded && !screenMinimized && (
                <div
                  className="screen-resize"
                  title={t.call.resizeHint}
                  onPointerDown={startScreenResize}
                />
              )}
            </motion.div>
          </div>
        )}
      </AnimatePresence>
    </div>
  );
}

function MessageBubble({ m, onDelete, onReact, onOpenMedia, myPeerId, t }) {
  const [menuOpen, setMenuOpen] = useState(false);

  const handleMediaClick = () => {
    if (onOpenMedia && m.mediaKind) onOpenMedia(m);
  };

  return (
    <div
      className={"bubble-wrap " + (m.out ? "out" : "in")}
      onContextMenu={(e) => {
        e.preventDefault();
        setMenuOpen(true);
      }}
    >
      <motion.div
        className={"bubble " + (m.out ? "out" : "in")}
        initial={{ opacity: 0, y: 8 }}
        animate={{ opacity: 1, y: 0 }}
        exit={{ opacity: 0, scale: 0.9 }}
        transition={{ duration: 0.2 }}
      >
        {m.mediaKind ? (
          m.mediaKind === "video" ? (
            <video
              src={m.mediaData}
              controls
              className="msg-media"
              onClick={handleMediaClick}
              style={{ cursor: "pointer" }}
            />
          ) : (
            <img
              src={m.mediaData}
              className="msg-media"
              alt=""
              onClick={handleMediaClick}
              style={{ cursor: "pointer" }}
            />
          )
        ) : (
          <span className="bubble-text">{m.text}</span>
        )}

        <span className="bubble-meta">
          <span className="bubble-time">
            {new Date(m.ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
          </span>
          {m.out && (
            <span
              className={
                "read-ticks " +
                (m.read ? "read" : "") +
                (m.delivered === false ? " pending" : "")
              }
            >
              {m.delivered === false ? "🕐" : m.read ? "✓✓" : "✓"}
            </span>
          )}
        </span>
      </motion.div>

      {/* реакции: моя (m.reaction) и собеседника (m.reactionPeer) */}
      {(m.reaction || m.reactionPeer) && (
        <div className="bubble-reaction">
          {[m.reaction, m.reactionPeer].filter(Boolean).join(" ")}
        </div>
      )}

      {menuOpen && (
        <div className="msg-menu glass-strong" onMouseLeave={() => setMenuOpen(false)}>
          {/* NEW: панель реакций встроена в то же контекстное меню, что и
              удаление — по требованию "не должна ломать контекстное меню". */}
          <div className="msg-menu-reactions">
            {REACTIONS.map((r) => (
              <button
                key={r}
                type="button"
                className={"reaction-btn " + (m.reaction === r ? "active" : "")}
                onClick={() => {
                  onReact && onReact(m.id, m.reaction === r ? "" : r);
                  setMenuOpen(false);
                }}
              >
                {r}
              </button>
            ))}
          </div>

          {m.out && (
            <div
              className="msg-menu-item"
              onClick={() => {
                onDelete(m.id, "everyone");
                setMenuOpen(false);
              }}
            >
              {t.chat.deleteForBoth}
            </div>
          )}
          <div
            className="msg-menu-item"
            onClick={() => {
              onDelete(m.id, "me");
              setMenuOpen(false);
            }}
          >
            {t.chat.deleteForMe}
          </div>
        </div>
      )}
    </div>
  );
}

function ChatWindow({
  chat,
  messages,
  myPeerId,
  connStatus,
  onOpenProfile,
  onSend,
  onDeleteMessage,
  onReact,
  onOpenMedia,
  onMarkRead,
  onTyping,
  isPeerTyping,
  blocked,
  pingByPeer,
  onBack,
  t,
}) {
  const [text, setText] = useState("");
  const [emojiOpen, setEmojiOpen] = useState(false);
  const fileRef = useRef(null);
  const scrollRef = useRef(null);
  const inputRef = useRef(null);

  useEffect(() => {
    scrollRef.current?.scrollTo({
      top: scrollRef.current.scrollHeight,
      behavior: "smooth",
    });
  }, [messages.length]);

  useEffect(() => {
    if (chat?.id) onMarkRead(chat.id);
  }, [chat?.id, messages.length, onMarkRead]);

  // сбрасываем локальный "я печатаю" при смене чата
  useEffect(() => {
    setText("");
    setEmojiOpen(false);
    return () => {
      if (onTyping) onTyping(false);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chat?.id]);

  // Закрываем панель эмодзи по клику вне её и по Escape.
  useEffect(() => {
    if (!emojiOpen) return;
    const onDown = (e) => {
      if (!e.target.closest?.(".emoji-popover") && !e.target.closest?.(".emoji-btn")) {
        setEmojiOpen(false);
      }
    };
    const onKey = (e) => e.key === "Escape" && setEmojiOpen(false);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [emojiOpen]);

  if (!chat) {
    return (
      <div className="main-panel glass">
        <div className="empty-hint" style={{ margin: "auto" }}>
          {t.pickChatHint}
        </div>
      </div>
    );
  }

  const send = () => {
    if (!text.trim()) return;
    onSend({ text, mediaKind: "", mediaData: "" });
    setText("");
    if (onTyping) onTyping(false);
  };

  const handleChangeText = (e) => {
    const value = e.target.value;
    setText(value);
    if (onTyping) onTyping(value.trim().length > 0);
  };

  const insertEmoji = (emoji) => {
    const input = inputRef.current;
    const next =
      input && typeof input.selectionStart === "number"
        ? text.slice(0, input.selectionStart) + emoji + text.slice(input.selectionEnd)
        : text + emoji;
    const caret =
      input && typeof input.selectionStart === "number"
        ? input.selectionStart + emoji.length
        : next.length;

    setText(next);
    if (onTyping) onTyping(next.trim().length > 0);
    requestAnimationFrame(() => {
      input?.focus();
      input?.setSelectionRange?.(caret, caret);
    });
  };

  const attach = (e) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;

    if (file.size > MAX_ATTACHMENT_BYTES) {
      alert(t.attachTooLarge);
      return;
    }

    const reader = new FileReader();
    reader.onload = () => {
      onSend({
        text: "",
        mediaKind: file.type.startsWith("video") ? "video" : "image",
        mediaData: reader.result,
      });
    };
    reader.readAsDataURL(file);
  };

  const displayMessages = (Array.isArray(messages) ? messages : []).map((m) => ({
    ...m,
    out: m.senderId === myPeerId,
  }));

  const typeLabel =
    chat.type === "saved"
      ? t.savedNotes
      : isPeerTyping
        ? t.typing
        : chat.online
          ? t.online
          : t.offline;

  return (
    <div className="main-panel glass">
      <div className="chat-header" onClick={() => onOpenProfile(chat, false)}>
        {isMobile && (
          <button
            type="button"
            className="chat-back"
            aria-label={t.back}
            onClick={(e) => {
              e.stopPropagation();
              onBack?.();
            }}
          >
            ‹
          </button>
        )}
        <Avatar name={chat.title} avatar={chat.avatar} online={chat.online} />
        <div className="chat-header-info">
          <div className="chat-header-name">{chat.title}</div>
          <div className={"chat-preview " + (isPeerTyping ? "typing-preview" : "")}>
            {blocked ? t.profile.blockedLabel : typeLabel}
          </div>
        </div>
        <div className="chat-header-right">
          {chat.peerId && pingByPeer[chat.peerId] != null && (
            <span className="ping-badge">{pingByPeer[chat.peerId]} ms</span>
          )}
          <ConnectionBadge status={connStatus} t={t} />
        </div>
      </div>

      <div className="messages" ref={scrollRef}>
        <AnimatePresence>
          {displayMessages.map((m) => (
            <MessageBubble
              key={m.id}
              m={m}
              onDelete={onDeleteMessage}
              onReact={onReact}
              onOpenMedia={onOpenMedia}
              myPeerId={myPeerId}
              t={t}
            />
          ))}
        </AnimatePresence>
      </div>

      <div className="composer">
        {blocked ? (
          <div className="blocked-notice">{t.profile.blockedComposer}</div>
        ) : (
          <>
            <input
              ref={fileRef}
              type="file"
              accept="image/*,video/*"
              style={{ display: "none" }}
              onChange={attach}
            />
            <button
              type="button"
              className="attach-btn"
              onClick={() => fileRef.current?.click()}
            >
              📎
            </button>
            <button
              type="button"
              className={"emoji-btn " + (emojiOpen ? "open" : "")}
              title={t.emojiTitle}
              aria-label={t.emojiTitle}
              onClick={() => setEmojiOpen((v) => !v)}
            >
              🙂
            </button>

            <AnimatePresence>
              {emojiOpen && (
                <motion.div
                  className="emoji-popover glass-strong"
                  initial={{ opacity: 0, y: 8, scale: 0.96 }}
                  animate={{ opacity: 1, y: 0, scale: 1 }}
                  exit={{ opacity: 0, y: 8, scale: 0.96 }}
                  transition={{ duration: 0.18, ease: [0.22, 1, 0.36, 1] }}
                >
                  <div className="emoji-popover-title">{t.emojiTitle}</div>
                  <div className="emoji-grid">
                    {EMOJIS.map((emoji) => (
                      <button
                        key={emoji}
                        type="button"
                        onClick={() => insertEmoji(emoji)}
                      >
                        {emoji}
                      </button>
                    ))}
                  </div>
                </motion.div>
              )}
            </AnimatePresence>

            <input
              ref={inputRef}
              type="text"
              placeholder={t.composerPlaceholder}
              value={text}
              onChange={handleChangeText}
              onKeyDown={(e) => e.key === "Enter" && send()}
            />
            <motion.button
              type="button"
              className="send-btn"
              onClick={send}
              whileTap={{ scale: 0.85 }}
            >
              ➤
            </motion.button>
          </>
        )}
      </div>
    </div>
  );
}

function SettingsPanel({
  platform,
  theme,
  setTheme,
  lang,
  setLang,
  profile,
  setProfile,
  onClose,
  connStatus,
  onLogout,
  t,
}) {
  const [version, setVersion] = useState("");
  const [dataDir, setDataDir] = useState("");
  const [screenQuality, setScreenQuality] = useState(loadScreenQuality);
  const [audioInputs, setAudioInputs] = useState([]);
  const [micDevice, setMicDevice] = useState(loadMicDevice);
  // Kept as a draft and written only on Save, so it is obvious whether a value
  // has actually been applied.
  const [ice, setIce] = useState(loadIceConfig);
  const [iceSaved, setIceSaved] = useState(loadIceConfig);
  const [iceNote, setIceNote] = useState("");

  const iceDirty =
    ice.turnUrl !== iceSaved.turnUrl ||
    ice.turnUser !== iceSaved.turnUser ||
    ice.turnPass !== iceSaved.turnPass;

  const turnLooksWrong =
    ice.turnUrl.trim() !== "" && !/^(turns?:)?[^\s:]+(:\d+)?/i.test(ice.turnUrl.trim());

  const commitIce = () => {
    const cleaned = {
      turnUrl: normalizeTurnUrl(ice.turnUrl),
      turnUser: ice.turnUser.trim(),
      turnPass: ice.turnPass,
    };
    // Clearing the address removes TURN entirely rather than half-saving it.
    if (!cleaned.turnUrl) {
      cleaned.turnUser = "";
      cleaned.turnPass = "";
    }
    saveIceConfig(cleaned);
    setIce(cleaned);
    setIceSaved(cleaned);
    setIceNote(cleaned.turnUrl ? t.settings.turnSaved : t.settings.turnCleared);
    setTimeout(() => setIceNote(""), 2500);
  };

  const updateMicDevice = (id) => {
    setMicDevice(id);
    saveMicDevice(id);
  };

  useEffect(() => {
    // Labels are only exposed once mic permission has been granted, which is
    // why the list can look anonymous before the first call.
    navigator.mediaDevices
      ?.enumerateDevices?.()
      .then((devices) =>
        setAudioInputs(devices.filter((d) => d.kind === "audioinput"))
      )
      .catch(() => {});
  }, []);

  const updateScreenQuality = (patch) => {
    const next = { ...screenQuality, ...patch };
    setScreenQuality(next);
    saveScreenQuality(next);
  };

  useEffect(() => {
    WailsApp.AppVersion().then(setVersion).catch(() => {});
    WailsApp.GetDataDir().then(setDataDir).catch(() => {});
  }, []);

  const save = async (updated) => {
    setProfile(updated);
    try {
      await WailsApp.UpdateProfile(updated);
    } catch (err) {
      console.error("UpdateProfile failed:", err);
    }
  };

  return (
    <div className="main-panel glass settings-panel">
      <div className="close-settings" onClick={onClose}>
        ‹ {t.settings.close}
      </div>
      <div className="settings-title">{t.settings.title}</div>

      <div className="settings-group-title">{t.settings.appearance}</div>
      <div className="settings-row">
        <label>{t.settings.theme}</label>
        <select value={theme} onChange={(e) => setTheme(e.target.value)}>
          {THEMES.map((name) => (
            <option key={name} value={name}>
              {THEME_ICON[name] + "  " + t.theme[name]}
            </option>
          ))}
        </select>
      </div>
      <div className="settings-row">
        <label>{t.settings.language}</label>
        <select value={lang} onChange={(e) => setLang(e.target.value)}>
          <option value="ru">Русский</option>
          <option value="en">English</option>
        </select>
      </div>

      <div className="settings-group-title">{t.settings.connection}</div>
      <div className="settings-row">
        <label>{t.connStatus[connStatus]}</label>
      </div>

      <div className="settings-group-title">{t.settings.profile}</div>
      <div className="settings-row">
        <label>{t.settings.name}</label>
        <input
          type="text"
          value={profile.name}
          onChange={(e) => save({ ...profile, name: e.target.value })}
        />
      </div>
      <div className="settings-row">
        <label>{t.settings.nickname}</label>
        <input
          type="text"
          value={profile.username}
          onChange={(e) => save({ ...profile, username: e.target.value })}
        />
      </div>
      <div className="settings-row">
        <label>{t.settings.bio}</label>
        <textarea
          value={profile.bio}
          onChange={(e) => save({ ...profile, bio: e.target.value })}
        />
      </div>
      <div className="settings-row">
        <label>{t.settings.peerId}</label>
        <span className="peer-id-value">{profile.peerId}</span>
      </div>

      {/* Version + data folder make it obvious which build is running and where
          its local database lives (stale side-by-side installs are otherwise
          invisible). */}
      <div className="settings-group-title">{t.settings.audio}</div>
      <div className="settings-row">
        <label>{t.settings.micDevice}</label>
        <select value={micDevice} onChange={(e) => updateMicDevice(e.target.value)}>
          <option value="">{t.settings.micDefault}</option>
          {audioInputs.map((d, i) => (
            <option key={d.deviceId || i} value={d.deviceId}>
              {d.label || `Audio input ${i + 1}`}
            </option>
          ))}
        </select>
      </div>
      <div className="settings-hint">{t.settings.micHint}</div>

      <div className="settings-group-title">{t.settings.calls}</div>
      <div className="settings-row">
        <label>{t.settings.turnUrl}</label>
        <input
          type="text"
          value={ice.turnUrl}
          placeholder={t.settings.turnUrlPlaceholder}
          onChange={(e) => setIce({ ...ice, turnUrl: e.target.value })}
        />
      </div>
      <div className="settings-row">
        <label>{t.settings.turnUser}</label>
        <input
          type="text"
          value={ice.turnUser}
          onChange={(e) => setIce({ ...ice, turnUser: e.target.value })}
        />
      </div>
      <div className="settings-row">
        <label>{t.settings.turnPass}</label>
        <input
          type="password"
          value={ice.turnPass}
          onChange={(e) => setIce({ ...ice, turnPass: e.target.value })}
        />
      </div>
      <div className="settings-row settings-actions">
        <span className={"settings-note " + (iceNote ? "shown" : "")}>
          {iceNote || (turnLooksWrong ? t.settings.turnInvalid : "")}
        </span>
        <button
          type="button"
          className="theme-toggle"
          disabled={!iceDirty}
          onClick={commitIce}
        >
          {t.settings.turnSave}
        </button>
      </div>
      <div className="settings-hint">{t.settings.turnHint}</div>

      <div className="settings-group-title">{t.settings.screenShare}</div>
      <div className="settings-row">
        <label>{t.settings.screenResolution}</label>
        <select
          value={screenQuality.height}
          onChange={(e) => updateScreenQuality({ height: Number(e.target.value) })}
        >
          {SCREEN_HEIGHTS.map((h) => (
            <option key={h} value={h}>
              {h}p
            </option>
          ))}
        </select>
      </div>
      <div className="settings-row">
        <label>{t.settings.screenFps}</label>
        <select
          value={screenQuality.fps}
          onChange={(e) => updateScreenQuality({ fps: Number(e.target.value) })}
        >
          {SCREEN_FPS.map((f) => (
            <option key={f} value={f}>
              {f} fps
            </option>
          ))}
        </select>
      </div>
      <div className="settings-row">
        <label>{t.settings.screenMode}</label>
        <select
          value={screenQuality.mode}
          onChange={(e) => updateScreenQuality({ mode: e.target.value })}
        >
          <option value="balanced">{t.settings.screenModeBalanced}</option>
          <option value="detail">{t.settings.screenModeDetail}</option>
          <option value="motion">{t.settings.screenModeMotion}</option>
        </select>
      </div>
      <div className="settings-row">
        <label>{t.settings.screenBitrate}</label>
        <input
          type="range"
          min="2"
          max="30"
          step="1"
          value={screenQuality.bitrate}
          onChange={(e) => updateScreenQuality({ bitrate: Number(e.target.value) })}
        />
        <span className="settings-value" style={{ direction: "ltr", maxWidth: 90 }}>
          {screenQuality.bitrate} Mbps
        </span>
      </div>
      <div className="settings-row">
        <label>{t.settings.screenAudio}</label>
        <select
          value={screenQuality.audioSource}
          onChange={(e) => updateScreenQuality({ audioSource: e.target.value })}
        >
          <option value="">{t.settings.screenAudioSystem}</option>
          <option value="none">{t.settings.screenAudioNone}</option>
          {audioInputs.map((d, i) => (
            <option key={d.deviceId || i} value={d.deviceId}>
              {d.label || `Audio input ${i + 1}`}
            </option>
          ))}
        </select>
      </div>
      <div className="settings-hint">{t.settings.screenHint}</div>
      {platform === "darwin" && (
        <div className="settings-hint">{t.settings.screenAudioMacHint}</div>
      )}

      <div className="settings-group-title">{t.settings.about}</div>
      <div className="settings-row">
        <label>{t.settings.version}</label>
        <span className="peer-id-value">{version || "…"}</span>
      </div>
      {can("openDataFolder") && (
        <div className="settings-row">
          <label>{t.settings.dataFolder}</label>
          <span className="settings-value" title={dataDir}>
            {dataDir || "…"}
          </span>
          <button
            type="button"
            className="theme-toggle"
            onClick={() => WailsApp.OpenDataFolder().catch(() => {})}
          >
            📂 {t.settings.openFolder}
          </button>
        </div>
      )}

      <div className="settings-group-title">{t.settings.dangerZone}</div>
      <div className="settings-row">
        <label>{t.settings.logoutHint}</label>
        <button type="button" className="theme-toggle danger" onClick={onLogout}>
          {t.settings.logout}
        </button>
      </div>
    </div>
  );
}

function ProfilePanel({
  target,
  isMe,
  onClose,
  onSave,
  onOpenChat,
  onOpenMedia,
  onCall,
  onBlock,
  isBlocked,
  t,
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState({
    name: target.name || target.title,
    username: target.username || "",
    bio: target.bio || "",
    avatar: target.avatar || null,
  });
  const fileRef = useRef(null);
  const displayName = editing ? draft.name : target.name || target.title;

  const save = () => {
    onSave(draft);
    setEditing(false);
  };

  const pickAvatar = (e) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => setDraft((d) => ({ ...d, avatar: reader.result }));
    reader.readAsDataURL(file);
  };

  return (
    <motion.div
      className="profile-overlay"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      onClick={onClose}
    >
      <motion.div
        className="profile-panel glass-strong"
        initial={{ x: 360 }}
        animate={{ x: 0 }}
        exit={{ x: 360 }}
        transition={{ type: "spring", stiffness: 260, damping: 26 }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="profile-header">
          <div className="profile-back" onClick={onClose}>
            ‹ {t.profile.back}
          </div>
          {isMe && (
            <div className="profile-edit" onClick={() => (editing ? save() : setEditing(true))}>
              {editing ? t.profile.done : t.profile.edit}
            </div>
          )}
        </div>

        <div className="profile-hero">
          <div
            onClick={() => editing && fileRef.current?.click()}
            style={{ cursor: editing ? "pointer" : "default" }}
          >
            <Avatar name={displayName} avatar={draft.avatar} size="lg" />
          </div>

          {editing && (
            <input
              ref={fileRef}
              type="file"
              accept="image/*"
              style={{ display: "none" }}
              onChange={pickAvatar}
            />
          )}

          {editing ? (
            <input
              type="text"
              value={draft.name}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
              style={{ textAlign: "center", fontSize: 17, fontWeight: 700 }}
            />
          ) : (
            <div className="profile-name">{displayName}</div>
          )}

          <div className="profile-status">
            {isBlocked ? t.profile.blockedLabel : t.profile.lastSeen}
          </div>
        </div>

        {!isMe && (
          <div className="profile-actions">
            <button type="button" className="profile-action-btn" onClick={onOpenChat}>
              💬<span>{t.profile.chat}</span>
            </button>
            <button type="button" className="profile-action-btn" onClick={() => onCall(false)}>
              📞<span>{t.profile.call}</span>
            </button>
            <button type="button" className="profile-action-btn" onClick={onOpenMedia}>
              🖼<span>{t.profile.media}</span>
            </button>
            <button
              type="button"
              className={"profile-action-btn " + (isBlocked ? "unblock" : "block")}
              onClick={onBlock}
            >
              {isBlocked ? "✓" : "⛔"}
              <span>{isBlocked ? t.profile.unblock : t.profile.blockAction}</span>
            </button>
          </div>
        )}

        <div className="profile-info-block">
          <div className="profile-info-row">
            <div className="profile-info-label">{t.profile.username}</div>
            {editing ? (
              <input
                type="text"
                value={draft.username}
                onChange={(e) => setDraft({ ...draft, username: e.target.value })}
              />
            ) : (
              <div className="profile-info-value">{draft.username || "—"}</div>
            )}
          </div>

          <div className="profile-info-row">
            <div className="profile-info-label">{t.profile.about}</div>
            {editing ? (
              <textarea
                value={draft.bio}
                onChange={(e) => setDraft({ ...draft, bio: e.target.value })}
              />
            ) : (
              <div className="profile-info-value plain">{draft.bio || "—"}</div>
            )}
          </div>

          {target.peerId && (
            <div className="profile-info-row">
              <div className="profile-info-label">{t.profile.peerId}</div>
              <div className="profile-info-value plain">{target.peerId}</div>
            </div>
          )}
        </div>
      </motion.div>
    </motion.div>
  );
}

export default function App() {
  const [theme, setTheme] = useState(() => readStored("cloudix:theme", "dark", THEMES));
  const [lang, setLang] = useState(() => readStored("cloudix:lang", "ru", ["ru", "en"]));
  const [isMaximized, setIsMaximized] = useState(false);

  useEffect(() => {
    try {
      localStorage.setItem("cloudix:theme", theme);
    } catch {}
  }, [theme]);

  useEffect(() => {
    try {
      localStorage.setItem("cloudix:lang", lang);
    } catch {}
  }, [lang]);

  // Platform drives window-chrome CSS: macOS keeps the traffic-light inset and
  // the rounded shell; Windows/Linux drop both (the OS draws the frame there, so
  // our own rounding shows black corners and the inset is dead space).
  const [platform, setPlatform] = useState(guessPlatform);

  useEffect(() => {
    Environment()
      .then((env) => {
        const p = (env?.platform || "").toLowerCase() || guessPlatform();
        setPlatform(p);
        document.documentElement.setAttribute("data-platform", p);
      })
      .catch(() => {
        document.documentElement.setAttribute("data-platform", guessPlatform());
      });
  }, []);

  useEffect(() => {
    const checkMaximized = () => {
      const maximized =
        window.innerWidth >= window.screen.availWidth &&
        window.innerHeight >= window.screen.availHeight;
      setIsMaximized(maximized);
    };
    checkMaximized();
    window.addEventListener("resize", checkMaximized);
    return () => window.removeEventListener("resize", checkMaximized);
  }, []);
  const t = useT(lang);

  const [profile, setProfileState] = useState(null);
  const [bootLoading, setBootLoading] = useState(true);
  const [chatsRaw, setChatsRaw] = useState([]);
  const [messagesByChat, setMessagesByChat] = useState({});
  const [blocked, setBlocked] = useState([]);

  // NEW: локальный чат "Избранное" — хранится в localStorage, не трогает бэкенд
  const [savedMessages, setSavedMessages] = useState([]);
  const savedLoadedRef = useRef(false);

  useEffect(() => {
    savedLoadedRef.current = false;
    if (!profile?.peerId) {
      setSavedMessages([]);
      savedLoadedRef.current = true;
      return;
    }
    try {
      const raw = localStorage.getItem(savedStorageKey(profile.peerId));
      setSavedMessages(raw ? JSON.parse(raw) : []);
    } catch {
      setSavedMessages([]);
    } finally {
      savedLoadedRef.current = true;
    }
  }, [profile?.peerId]);

  useEffect(() => {
    if (!profile?.peerId) return;
    if (!savedLoadedRef.current) return;
    try {
      localStorage.setItem(savedStorageKey(profile.peerId), JSON.stringify(savedMessages));
    } catch {}
  }, [savedMessages, profile?.peerId]);

  // NEW: индикатор "печатает…" по каждому пиру
  const [typingByPeer, setTypingByPeer] = useState({});
  const [pingByPeer, setPingByPeer] = useState({});

  const [tab, setTab] = useState("all");
  const [search, setSearch] = useState("");
  const [activeChat, setActiveChat] = useState(null);
  const [showSettings, setShowSettings] = useState(false);
  const [showNetwork, setShowNetwork] = useState(false);
  const [showDocs, setShowDocs] = useState(false);
  const [vpnStatus, setVpnStatus] = useState(null);
  const [connStatus, setConnStatus] = useState("connecting");
  const [onlinePeersRaw, setOnlinePeersRaw] = useState([]);
  const [profileTarget, setProfileTarget] = useState(null);
  const [profileIsMe, setProfileIsMe] = useState(false);
  const [mediaChatId, setMediaChatId] = useState(null);
  const [callState, setCallState] = useState(null);
  // NEW: полноэкранный просмотр медиа
  const [viewerItem, setViewerItem] = useState(null);

  const signalHandlersRef = useRef(new Set());
  const pendingSignalsRef = useRef([]);
  const blockedRef = useRef(blocked);
  const chatsRawRef = useRef(chatsRaw);
  const onlinePeersRawRef = useRef(onlinePeersRaw);
  const activeCallIdRef = useRef(null);
  const typingSendTimerRef = useRef(null);
  // peerId -> timeout: auto-clears a stuck "typing…" if the "stopped" signal
  // never arrives (peer app crashed / lost connection mid-typing).
  const typingClearTimersRef = useRef({});

  useEffect(() => {
    blockedRef.current = blocked;
  }, [blocked]);

  useEffect(() => {
    chatsRawRef.current = chatsRaw;
  }, [chatsRaw]);

  useEffect(() => {
    onlinePeersRawRef.current = onlinePeersRaw;
  }, [onlinePeersRaw]);

  useEffect(() => {
    activeCallIdRef.current = callState?.callId || null;
  }, [callState]);

  // FIX: раньше "онлайн" статус чатов считался через список, УЖЕ
  // отфильтрованный по блокировке (visibleOnlinePeers). Из-за этого при
  // блокировке собеседника его онлайн-статус пропадал даже когда он
  // реально в сети. Теперь online считается по полному списку onlinePeersRaw,
  // а блокировка влияет только на видимость во вкладке "В сети" и на
  // возможность переписки/звонков.
  const visibleOnlinePeers = useMemo(
    () => (onlinePeersRaw || []).filter((p) => !blocked.includes(p.peerId)),
    [onlinePeersRaw, blocked]
  );

  // An incoming offer creates callState, but CallModal only registers its
  // handler once React has mounted it. Every signal that lands in that gap —
  // in practice the first burst of ICE candidates — used to be dropped on the
  // floor, which is why calls connected only occasionally. Buffer them and
  // replay to the first handler that registers.
  const registerSignalHandler = useCallback((handler) => {
    signalHandlersRef.current.add(handler);

    const queued = pendingSignalsRef.current;
    pendingSignalsRef.current = [];
    queued.forEach((payload) => {
      try {
        handler(payload);
      } catch (err) {
        console.error("replaying buffered signal failed:", err);
      }
    });

    return () => signalHandlersRef.current.delete(handler);
  }, []);

  useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
  }, [theme]);

  const refreshChats = useCallback(async () => {
    try {
      const chats = await WailsApp.GetChats();
      setChatsRaw(Array.isArray(chats) ? chats : []);
    } catch (err) {
      console.error("GetChats failed:", err);
      setChatsRaw([]);
    }
  }, []);

  // FIX: раньше метод назывался GetDiscoveredPeers на бэкенде, а тут ожидался
  // GetOnlinePeers — проверка typeof тихо проваливалась и весь polling не
  // делал ничего. Метод на бэкенде переименован (см. app.go), теперь polling
  // реально работает и подхватывает уже онлайн пиров сразу после регистрации.
  const refreshOnlinePeers = useCallback(async () => {
    try {
      const peers = await WailsApp.GetOnlinePeers();
      setOnlinePeersRaw(Array.isArray(peers) ? peers : []);
    } catch (err) {
      console.error("GetOnlinePeers failed:", err);
    }
  }, []);

  const refreshVpnStatus = useCallback(async () => {
    try {
      setVpnStatus(await WailsApp.VPNStatus());
    } catch (err) {
      console.error("VPNStatus failed:", err);
    }
  }, []);

  const loadMessages = useCallback(async (peerId) => {
    if (peerId === SAVED_CHAT_ID) return;
    try {
      const msgs = await WailsApp.GetMessages(peerId);
      setMessagesByChat((prev) => ({ ...prev, [peerId]: Array.isArray(msgs) ? msgs : [] }));
    } catch (err) {
      console.error("GetMessages failed:", err);
      setMessagesByChat((prev) => ({ ...prev, [peerId]: [] }));
    }
  }, []);

  useEffect(() => {
    (async () => {
      try {
        const p = await WailsApp.GetProfile();
        if (p && p.peerId) {
          setProfileState(p);
          await refreshChats();
          const bl = await WailsApp.ListBlocked();
          setBlocked(Array.isArray(bl) ? bl : []);
        }
      } catch (err) {
        console.error("GetProfile failed:", err);
      } finally {
        setBootLoading(false);
      }
    })();
  }, [refreshChats]);

  useEffect(() => {
    if (!profile?.peerId) return;

    refreshOnlinePeers();
    refreshChats();
    refreshVpnStatus();

    const t1 = setTimeout(() => {
      refreshOnlinePeers();
      refreshChats();
    }, 600);
    const t2 = setTimeout(() => {
      refreshOnlinePeers();
      refreshChats();
    }, 1800);
    const t3 = setTimeout(() => {
      refreshOnlinePeers();
    }, 3500);

    const pingInterval = setInterval(() => {
      onlinePeersRawRef.current.forEach((p) => {
        WailsApp.SendPing(p.peerId).catch(() => {});
      });
    }, 5000);

    return () => {
      clearTimeout(t1);
      clearTimeout(t2);
      clearTimeout(t3);
      clearInterval(pingInterval);
    };
  }, [profile?.peerId, refreshChats, refreshOnlinePeers, refreshVpnStatus]);

  useEffect(() => {
    if (!profile) return;

    let cancelled = false;
    WailsApp.NetworkReady()
      .then((ready) => {
        if (!cancelled) setConnStatus(ready ? "connected" : "disconnected");
      })
      .catch(() => {
        if (!cancelled) setConnStatus("connected");
      });

    const cancelPeers = EventsOn("peers:update", (peers) => {
      setOnlinePeersRaw(Array.isArray(peers) ? peers : []);
    });

    const cancelIncoming = EventsOn("message:incoming", (msg = {}) => {
      if (!msg.chatId) return;
      setMessagesByChat((prev) => {
        const list = prev[msg.chatId] || [];
        if (list.some((m) => m.id === msg.id)) return prev;
        return { ...prev, [msg.chatId]: sortByTs([...list, msg]) };
      });
      refreshChats();
    });

    const cancelDelivered = EventsOn("message:delivered", ({ chatId, id } = {}) => {
      if (!chatId || !id) return;
      setMessagesByChat((prev) => ({
        ...prev,
        [chatId]: (prev[chatId] || []).map((m) =>
          m.id === id ? { ...m, delivered: true } : m
        ),
      }));
    });

    const cancelDeleted = EventsOn("message:deleted", ({ chatId, id } = {}) => {
      if (!chatId || !id) return;
      setMessagesByChat((prev) => ({
        ...prev,
        [chatId]: (prev[chatId] || []).filter((m) => m.id !== id),
      }));
    });

    const cancelRead = EventsOn("message:read", ({ chatId, ids } = {}) => {
      if (!chatId) return;
      const safeIds = Array.isArray(ids) ? ids : [];
      setMessagesByChat((prev) => ({
        ...prev,
        [chatId]: (prev[chatId] || []).map((m) =>
          safeIds.includes(m.id) ? { ...m, read: true } : m
        ),
      }));
      refreshChats();
    });

    // реакция от собеседника — кладём в reactionPeer, чтобы не затирать свою
    const cancelReacted = EventsOn("message:reacted", ({ chatId, id, reactionPeer } = {}) => {
      if (!chatId || !id) return;
      setMessagesByChat((prev) => ({
        ...prev,
        [chatId]: (prev[chatId] || []).map((m) =>
          m.id === id ? { ...m, reactionPeer: reactionPeer || "" } : m
        ),
      }));
    });

    const cancelPing = EventsOn("ping:result", ({ peerId, ms } = {}) => {
      if (!peerId) return;
      setPingByPeer((prev) => ({ ...prev, [peerId]: ms }));
    });

    const cancelVpn = EventsOn("vpn:status", (st) => {
      setVpnStatus(st || null);
      refreshOnlinePeers();
    });

    const cancelProfileUpdated = EventsOn("profile:updated", () => refreshChats());
    const cancelAccountDeleted = EventsOn("account:deleted", () => refreshChats());

    const onSignalIncoming = (payload = {}) => {
      // NEW: индикатор "печатает…" приходит по этому же каналу, но не должен
      // попадать в логику звонков ниже.
      if (payload.kind === "typing") {
        try {
          const data = JSON.parse(payload.data || "{}");
          const isTyping = !!data.isTyping;
          const timers = typingClearTimersRef.current;
          if (timers[payload.peerId]) {
            clearTimeout(timers[payload.peerId]);
            delete timers[payload.peerId];
          }
          setTypingByPeer((prev) => ({ ...prev, [payload.peerId]: isTyping }));
          if (isTyping) {
            timers[payload.peerId] = setTimeout(() => {
              delete timers[payload.peerId];
              setTypingByPeer((prev) => ({ ...prev, [payload.peerId]: false }));
            }, 6000);
          }
        } catch {}
        return;
      }

      if (payload.kind === "offer") {
        if (activeCallIdRef.current && activeCallIdRef.current !== payload.callId) {
          WailsApp.SendSignal(payload.peerId, payload.callId, "reject", "", false).catch(() => {});
          return;
        }

        setCallState((prev) => {
          if (prev) return prev;

          const knownChat = chatsRawRef.current.find((c) => c.peerId === payload.peerId);
          const knownPeer = onlinePeersRawRef.current.find((p) => p.peerId === payload.peerId);

          return {
            target: {
              peerId: payload.peerId,
              name: knownChat?.name || knownPeer?.name || payload.peerId,
              title: knownChat?.name || knownPeer?.name || payload.peerId,
              avatar: knownChat?.avatar || knownPeer?.avatar || "",
              ip: payload.peerIp || knownPeer?.ip || "",
            },
            video: payload.video === true,
            isCaller: false,
            callId: payload.callId,
            incomingOffer: payload.data,
          };
        });
      }

      // The offer itself is already carried into CallModal as `incomingOffer`,
      // so it must not be replayed; everything else waits for a handler.
      if (signalHandlersRef.current.size === 0) {
        if (payload.kind !== "offer") {
          pendingSignalsRef.current.push(payload);
          if (pendingSignalsRef.current.length > 300) pendingSignalsRef.current.shift();
        }
        return;
      }

      signalHandlersRef.current.forEach((h) => h(payload));
    };

    const cancelSignal = EventsOn("signal:incoming", onSignalIncoming);

    // FIX: backend отправляет ошибку неудачной доставки сигнала как
    // ОТДЕЛЬНОЕ событие "signal:send_error" (см. app.go SendSignal — Send
    // выполняется в горутине, и при ошибке emitEvent идёт именно под этим
    // именем, а не через "signal:incoming"). Раньше этот listener не был
    // зарегистрирован вовсе, поэтому CallModal.handleSignal никогда не
    // получал kind === "send_error", и кнопка "Принять"/звонок при
    // недостижимом пире выглядели так, будто ничего не происходит.
    const cancelSignalError = EventsOn("signal:send_error", (payload = {}) => {
      console.error("signal:send_error", payload);
      signalHandlersRef.current.forEach((h) => h({ ...payload, kind: "send_error" }));
    });

    const netCheck = () => {
      if (typeof navigator !== "undefined" && "onLine" in navigator) {
        if (!navigator.onLine) {
          setConnStatus("reconnecting");
          setOnlinePeersRaw([]);
        } else {
          // FIX: простого navigator.onLine недостаточно — Go-стороне нужно
          // заново привязать multicast UDP listener к новому сетевому интерфейсу,
          // иначе discovery молча остаётся мёртвым при видимом "connected".
          setConnStatus("connected");
          WailsApp.RestartNetworking()
            .then(() => {
              refreshOnlinePeers();
              refreshChats();
            })
            .catch((err) => {
              console.error("RestartNetworking failed:", err);
            });
        }
      }
    };

    window.addEventListener("offline", netCheck);
    window.addEventListener("online", netCheck);

    return () => {
      cancelled = true;
      cancelPeers?.();
      cancelIncoming?.();
      cancelDelivered?.();
      cancelDeleted?.();
      cancelRead?.();
      cancelReacted?.();
      cancelPing?.();
      cancelVpn?.();
      cancelProfileUpdated?.();
      cancelAccountDeleted?.();
      cancelSignal?.();
      cancelSignalError?.();
      window.removeEventListener("offline", netCheck);
      window.removeEventListener("online", netCheck);
    };
  }, [profile, refreshChats, refreshOnlinePeers]);

  useEffect(() => {
    if (activeChat) loadMessages(activeChat);
  }, [activeChat, loadMessages]);

  const openProfile = (target, isMe) => {
    setProfileTarget(target);
    setProfileIsMe(isMe);
  };

  const saveProfile = async (draft) => {
    if (profileIsMe) {
      const updated = { ...profile, ...draft };
      setProfileState(updated);
      try {
        await WailsApp.UpdateProfile(updated);
      } catch (err) {
        console.error("UpdateProfile failed:", err);
      }
    } else if (profileTarget) {
      await refreshChats();
    }
  };

  const startChatWithPeer = async (peer) => {
    try {
      await WailsApp.StartChatWithPeer(
        peer.peerId,
        peer.name || "",
        peer.username || "",
        peer.bio || "",
        peer.avatar || ""
      );
      await refreshChats();
      setActiveChat(peer.peerId);
      setTab("all");
      setShowSettings(false);
    } catch (err) {
      console.error("startChatWithPeer failed:", err);
      alert(t.openChatError + (err?.message || err));
    }
  };

  const sendMessage = async (chatId, { text, mediaKind, mediaData }) => {
    try {
      const msg = await WailsApp.SendMessage(chatId, text, mediaKind, mediaData);
      setMessagesByChat((prev) => {
        const list = prev[chatId] || [];
        if (list.some((m) => m.id === msg.id)) return prev;
        return { ...prev, [chatId]: sortByTs([...list, msg]) };
      });
      refreshChats();
    } catch (err) {
      console.error("SendMessage failed:", err);
    }
  };

  // NEW: обёртка отправки — перенаправляет в локальное "Избранное", минуя бэкенд
  const sendMessageToChat = async (chatId, payload) => {
    if (chatId === SAVED_CHAT_ID) {
      const now = Date.now();
      const msg = {
        id: "saved-" + now + "-" + Math.random().toString(36).slice(2, 7),
        chatId,
        senderId: profile?.peerId,
        text: payload.text,
        mediaKind: payload.mediaKind,
        mediaData: payload.mediaData,
        ts: now,
        read: true,
      };
      setSavedMessages((prev) => [...prev, msg]);
      return;
    }
    await sendMessage(chatId, payload);
  };

  const deleteMessage = async (chatId, messageId, mode) => {
    try {
      await WailsApp.DeleteMessage(chatId, messageId, mode);
      setMessagesByChat((prev) => ({
        ...prev,
        [chatId]: (prev[chatId] || []).filter((m) => m.id !== messageId),
      }));
      refreshChats();
    } catch (err) {
      console.error("DeleteMessage failed:", err);
    }
  };

  // NEW: обёртка удаления — поддержка "Избранного"
  const deleteMessageFromChat = async (chatId, messageId, mode) => {
    if (chatId === SAVED_CHAT_ID) {
      setSavedMessages((prev) => prev.filter((m) => m.id !== messageId));
      return;
    }
    await deleteMessage(chatId, messageId, mode);
  };

  // NEW: реакции на сообщения
  const reactToMessage = async (chatId, messageId, reaction) => {
    if (chatId === SAVED_CHAT_ID) {
      setSavedMessages((prev) =>
        prev.map((m) => (m.id === messageId ? { ...m, reaction } : m))
      );
      return;
    }

    setMessagesByChat((prev) => ({
      ...prev,
      [chatId]: (prev[chatId] || []).map((m) =>
        m.id === messageId ? { ...m, reaction } : m
      ),
    }));

    try {
      await WailsApp.ReactToMessage(chatId, messageId, reaction);
    } catch (err) {
      console.error("ReactToMessage failed:", err);
    }
  };

  const toggleBlock = async (peerId) => {
    try {
      if (blockedRef.current.includes(peerId)) {
        await WailsApp.UnblockPeer(peerId);
        setBlocked((prev) => prev.filter((id) => id !== peerId));
      } else {
        await WailsApp.BlockPeer(peerId);
        setBlocked((prev) => [...prev, peerId]);
      }
      await refreshChats();
      await refreshOnlinePeers();
    } catch (err) {
      console.error("toggleBlock failed:", err);
    }
  };

  const markRead = useCallback(
    async (chatId) => {
      if (chatId === SAVED_CHAT_ID) return;
      try {
        await WailsApp.MarkChatRead(chatId);
        setMessagesByChat((prev) => ({
          ...prev,
          [chatId]: (prev[chatId] || []).map((m) =>
            m.senderId !== profile?.peerId ? { ...m, read: true } : m
          ),
        }));
        refreshChats();
      } catch (err) {
        console.error("MarkChatRead failed:", err);
      }
    },
    [refreshChats, profile?.peerId]
  );

  // Индикатор "печатает…": true уходит сразу (но не чаще раза в 2с), false —
  // с небольшим дебаунсом после остановки. Раньше и true отправлялся только
  // через 120мс ПОСЛЕ остановки ввода, поэтому индикатор почти не появлялся.
  const typingLastSentRef = useRef(0);
  const handleTyping = useCallback((peerId, isTyping) => {
    if (!peerId || peerId === SAVED_CHAT_ID) return;
    if (typingSendTimerRef.current) {
      clearTimeout(typingSendTimerRef.current);
      typingSendTimerRef.current = null;
    }

    const flush = (value) => {
      typingLastSentRef.current = value ? Date.now() : 0;
      WailsApp.SendTyping(peerId, value).catch((err) => {
        console.error("SendTyping failed:", err);
      });
    };

    if (isTyping) {
      if (Date.now() - typingLastSentRef.current > 2000) flush(true);
      // страховочный "перестал печатать", если ввод замер
      typingSendTimerRef.current = setTimeout(() => flush(false), 4000);
    } else {
      typingSendTimerRef.current = setTimeout(() => flush(false), 300);
    }
  }, []);

  // Manual "look for peers again": re-bind the discovery sockets on the Go side
  // (multicast membership is lost on interface changes / sleep), then re-poll.
  const rescanPeers = useCallback(async () => {
    try {
      await WailsApp.RestartNetworking();
    } catch (err) {
      console.error("RestartNetworking failed:", err);
    }
    try {
      const ready = await WailsApp.NetworkReady();
      setConnStatus(ready ? "connected" : "disconnected");
    } catch {}
    await refreshOnlinePeers();
    await refreshChats();
  }, [refreshOnlinePeers, refreshChats]);

  const startCall = (target, video) => {
    if (callState) return;

    const callId = "call-" + Date.now() + "-" + Math.random().toString(36).slice(2, 8);
    const knownPeer = onlinePeersRawRef.current.find((p) => p.peerId === target.peerId);
    setCallState({
      target: {
        peerId: target.peerId,
        name: target.name || target.title || target.peerId,
        title: target.title || target.name || target.peerId,
        avatar: target.avatar || "",
        // Seed for the mDNS ICE-candidate rewrite; refreshed from each signal.
        ip: knownPeer?.ip || "",
      },
      video,
      isCaller: true,
      callId,
      incomingOffer: null,
    });
    setProfileTarget(null);
  };

  const closeCall = useCallback(() => {
    pendingSignalsRef.current = [];
    setCallState(null);
  }, []);

  const logout = async () => {
    const prevPeerId = profile?.peerId;
    try {
      await WailsApp.Logout();
    } catch (err) {
      console.error("Logout failed:", err);
    }
    // "Удалить локальный профиль" должно чистить и локальное «Избранное».
    try {
      if (prevPeerId) localStorage.removeItem(savedStorageKey(prevPeerId));
    } catch {}
    setProfileState(null);
    setChatsRaw([]);
    setMessagesByChat({});
    setBlocked([]);
    setOnlinePeersRaw([]);
    setActiveChat(null);
    setShowSettings(false);
    setShowNetwork(false);
    setShowDocs(false);
    setVpnStatus(null);
    setProfileTarget(null);
    setMediaChatId(null);
    setCallState(null);
    setTypingByPeer({});
    setSavedMessages([]);
  };

  const chatsList = useMemo(() => {
    const savedChatMeta = {
      id: SAVED_CHAT_ID,
      type: "saved",
      title: t.savedNotes,
      name: t.savedNotes,
      username: "",
      bio: "",
      avatar: "",
      preview: savedMessages[savedMessages.length - 1]?.text || "",
      deleted: false,
      peerId: "",
      online: true,
      unread: 0,
    };

    const regularChats = (Array.isArray(chatsRaw) ? chatsRaw : []).map((c) => ({
      id: c?.peerId || "",
      type: "private",
      title: c?.name || c?.username || c?.peerId || "Unknown",
      name: c?.name || c?.username || c?.peerId || "Unknown",
      username: c?.username || "",
      bio: c?.bio || "",
      avatar: c?.avatar || "",
      preview: c?.lastMessage || "",
      deleted: !!c?.accountDeleted,
      peerId: c?.peerId || "",
      // FIX: считаем online по полному списку onlinePeersRaw, а не по
      // отфильтрованному blocked-списком visibleOnlinePeers (см. коммент
      // выше про visibleOnlinePeers).
      online: onlinePeersRaw.some((p) => p.peerId === c?.peerId),
      unread: c?.unread || 0,
    }));

    return [savedChatMeta, ...regularChats];
  }, [chatsRaw, onlinePeersRaw, savedMessages, t.savedNotes]);

  const activeChatMeta = activeChat ? chatsList.find((c) => c.id === activeChat) : null;
  const activeMessages =
    activeChat === SAVED_CHAT_ID
      ? savedMessages
      : activeChat
        ? messagesByChat[activeChat] || []
        : [];
  const activeIsBlocked = activeChatMeta ? blocked.includes(activeChatMeta.peerId) : false;
  const activeIsTyping =
    activeChatMeta && activeChatMeta.peerId ? !!typingByPeer[activeChatMeta.peerId] : false;
  const [showDisclaimer, setShowDisclaimer] = useState(false);

  if (bootLoading) return <div className="app-root" />;
  if (!profile)
    return (
      <Onboarding
        t={t}
        platform={platform}
        theme={theme}
        setTheme={setTheme}
        onDone={(p) => {
          setProfileState(p);
          setShowDisclaimer(true);
        }}
      />
    );
  if (showDisclaimer)
    return <DisclaimerModal t={t} platform={platform} onDismiss={() => setShowDisclaimer(false)} />;

  return (
    <div className={"app-root " + (isMaximized ? "maximized" : "")}>
      <AppTitlebar platform={platform} t={t} />
      {/* On a phone only one pane fits, and theme.css uses this to decide which:
          the chat list, or whatever has taken over the main panel. */}
      <div className="app-shell" data-chat-open={activeChat || showSettings ? "true" : "false"}>
        <Sidebar
          chats={chatsList}
          onlinePeers={visibleOnlinePeers}
          activeChat={activeChat}
          setActiveChat={(id) => {
            setActiveChat(id);
            setShowSettings(false);
          }}
          tab={tab}
          setTab={setTab}
          onOpenSettings={() => setShowSettings(true)}
          onOpenProfile={openProfile}
          t={t}
          myProfile={profile}
          search={search}
          setSearch={setSearch}
          onStartChatWithPeer={startChatWithPeer}
          typingByPeer={typingByPeer}
          theme={theme}
          setTheme={setTheme}
          onRescan={rescanPeers}
          onOpenNetwork={() => setShowNetwork(true)}
          onOpenDocs={() => setShowDocs(true)}
          netActive={!!vpnStatus?.active}
        />

        {showSettings ? (
          <SettingsPanel
            platform={platform}
            theme={theme}
            setTheme={setTheme}
            lang={lang}
            setLang={setLang}
            profile={profile}
            setProfile={setProfileState}
            onClose={() => setShowSettings(false)}
            connStatus={connStatus}
            onLogout={logout}
            t={t}
          />
        ) : (
          <ChatWindow
            chat={activeChatMeta}
            messages={activeMessages}
            myPeerId={profile.peerId}
            connStatus={connStatus}
            onOpenProfile={openProfile}
            onBack={() => setActiveChat(null)}
            onSend={(msg) => sendMessageToChat(activeChat, msg)}
            onDeleteMessage={(id, mode) => deleteMessageFromChat(activeChat, id, mode)}
            onReact={(id, reaction) => reactToMessage(activeChat, id, reaction)}
            onOpenMedia={(m) => setViewerItem(m)}
            onMarkRead={markRead}
            onTyping={(isTyping) =>
              activeChatMeta?.peerId && handleTyping(activeChatMeta.peerId, isTyping)
            }
            isPeerTyping={activeIsTyping}
            blocked={activeIsBlocked}
            pingByPeer={pingByPeer}
            t={t}
          />
        )}
      </div>

      <AnimatePresence>
        {profileTarget && (
          <ProfilePanel
            target={profileIsMe ? profile : profileTarget}
            isMe={profileIsMe}
            isBlocked={
              !profileIsMe && profileTarget.peerId ? blocked.includes(profileTarget.peerId) : false
            }
            onClose={() => setProfileTarget(null)}
            onSave={saveProfile}
            onOpenChat={() => {
              setActiveChat(profileTarget.id);
              setShowSettings(false);
              setProfileTarget(null);
            }}
            onOpenMedia={() => {
              setMediaChatId(profileTarget.id);
              setProfileTarget(null);
            }}
            onCall={(video) => startCall(profileTarget, video)}
            onBlock={() => toggleBlock(profileTarget.peerId)}
            t={t}
          />
        )}

        {mediaChatId && (
          <MediaPanel
            messages={messagesByChat[mediaChatId] || []}
            onClose={() => setMediaChatId(null)}
            onOpenMedia={(m) => setViewerItem(m)}
            t={t}
          />
        )}

        {showDocs && <DocsPanel t={t} onClose={() => setShowDocs(false)} />}

        {showNetwork && (
          <NetworkPanel
            status={vpnStatus}
            t={t}
            onClose={() => setShowNetwork(false)}
            onRefresh={async () => {
              await refreshVpnStatus();
              await refreshOnlinePeers();
            }}
          />
        )}

        {viewerItem && (
          <MediaViewer item={viewerItem} onClose={() => setViewerItem(null)} t={t} />
        )}

        {callState && (
          <CallModal
            key={callState.callId}
            target={callState.target}
            video={callState.video}
            isCaller={callState.isCaller}
            callId={callState.callId}
            incomingOffer={callState.incomingOffer}
            registerSignalHandler={registerSignalHandler}
            onClose={closeCall}
            t={t}
          />
        )}
      </AnimatePresence>
    </div>
  );
}