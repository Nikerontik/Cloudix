import React, { useState, useEffect, useRef, useCallback, useMemo } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { useT } from "./i18n";
import * as WailsApp from "../wailsjs/go/app/App";
import { EventsOn } from "../wailsjs/runtime/runtime";

const REACTIONS = ["👍", "❤️", "🔥", "😂", "👎"];
const SAVED_CHAT_ID = "__saved__";
const SAVED_STORAGE_KEY = "cloudix:saved-messages";

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

function Onboarding({ onDone }) {
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
      setError("Не удалось создать профиль. Попробуйте снова.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="onboarding-root">
      <div className="onboarding-titlebar" />
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
        <p className="onboarding-sub">
          Локальный P2P-мессенджер без сервера. Создайте профиль, чтобы начать общение в вашей сети.
        </p>

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
          <label>Имя</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Как вас называть?"
            autoFocus
          />
        </div>

        <div className="onboarding-field">
          <label>Юзернейм</label>
          <input
            type="text"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="@username"
          />
        </div>

        <div className="onboarding-field">
          <label>О себе (необязательно)</label>
          <input
            type="text"
            value={bio}
            onChange={(e) => setBio(e.target.value)}
            placeholder="Пара слов о себе"
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
          {busy ? "Создаём…" : "Начать общение"}
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
}) {
  // FIX: убраны вкладки "groups"/"channels" по запросу — они никогда не были
  // реализованы и только занимали место. "saved" тоже убрана как отдельная
  // вкладка — теперь это закреплённый чат сверху списка (см. App: savedChatMeta).
  const tabsOrder = ["all", "online"];

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
          </button>
        ))}
      </div>

      {tab === "online" ? (
        <div className="chat-list">
          {onlinePeers.length === 0 && (
            <div className="empty-hint">
              Никого не найдено в локальной сети.
              <br />
              Проверьте подключение к Wi‑Fi.
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
                <div className="chat-preview">{p.username} · найден в сети</div>
              </div>
            </motion.div>
          ))}
        </div>
      ) : (
        <div className="chat-list">
          {filtered.length === 0 && (
            <div className="empty-hint">
              Нет активных чатов.
              <br />
              Найдите собеседника во вкладке "В сети".
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
                    {c.deleted && <span className="deleted-tag"> · аккаунт удалён</span>}
                  </div>
                  <div className={"chat-preview " + (isTyping ? "typing-preview" : "")}>
                    {isTyping ? "печатает…" : c.preview || "Нет сообщений"}
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
        <button
          type="button"
          className="theme-toggle"
          onClick={onOpenSettings}
          style={{ width: "100%" }}
        >
          ⚙ {t.settingsBtn}
        </button>
      </div>
    </div>
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
function MediaViewer({ item, onClose }) {
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
          <a href={item.mediaData} download={fileName} className="theme-toggle">
            ⬇ Скачать
          </a>
          <button type="button" className="theme-toggle" onClick={onClose}>
            Закрыть
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

const RTC_CONFIG = { iceServers: [{ urls: "stun:stun.l.google.com:19302" }] };

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
  const [sharingScreen, setSharingScreen] = useState(false);
  const [seconds, setSeconds] = useState(0);
  const [remoteSharingScreen, setRemoteSharingScreen] = useState(false);
  const [errorText, setErrorText] = useState("");
  // NEW: возможность увеличить демонстрацию по двойному клику (object-fit
  // переключается между cover и contain, чтобы видеть кадр целиком).
  const [remoteZoomed, setRemoteZoomed] = useState(false);

  const pcRef = useRef(null);
  const localStreamRef = useRef(null);
  const screenStreamRef = useRef(null);
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

  const clearCallError = () => setErrorText("");

  const attachRemoteStream = async () => {
    try {
      if (remoteAudioRef.current) {
        remoteAudioRef.current.srcObject = remoteStreamRef.current;
        remoteAudioRef.current.muted = false;
        remoteAudioRef.current.volume = 1;
        await remoteAudioRef.current.play().catch(() => {});
      }
    } catch (err) {
      console.warn("attachRemoteStream (audio) failed:", err);
    }
  };

  const refreshRemoteVideoUi = async () => {
    const liveVideoTracks = remoteStreamRef.current
      .getVideoTracks()
      .filter((tr) => tr.readyState === "live" && tr.enabled !== false);

    const hasVideo = liveVideoTracks.length > 0;
    setRemoteSharingScreen(hasVideo);
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
      screenStreamRef.current?.getTracks().forEach((tr) => tr.stop());
    } catch {}
    try {
      remoteStreamRef.current?.getTracks().forEach((tr) => tr.stop());
    } catch {}
  };

  const resetCallUiState = () => {
    setPhase(isCaller ? "calling" : "ringing");
    setMuted(false);
    setSharingScreen(false);
    setRemoteSharingScreen(false);
    setRemoteZoomed(false);
    setSeconds(0);
    setErrorText("");
    pendingIceRef.current = [];
    earlyIceRef.current = [];
    makingOfferRef.current = false;
    ignoreOfferRef.current = false;
    startedRef.current = false;
  };

  // FIX: гарантированно вызываем onClose() в конце, оборачивая в try/catch —
  // раньше исключение в середине cleanupCall (например при закрытом
  // WailsApp.SendSignal) обрывало функцию ДО onClose(), из-за чего
  // activeCallIdRef в App не сбрасывался и следующий звонок отбрасывался
  // как "конкурирующий" (см. onSignalIncoming в App).
  const cleanupCall = useCallback(
    (notify = false, kind = "end") => {
      if (closedRef.current) return;
      closedRef.current = true;

      try {
        if (notify) {
          WailsApp.SendSignal(target.peerId, callId, kind, "", video || sharingScreen).catch(
            () => {}
          );
        }
      } catch {}

      try {
        unregisterRef.current?.();
      } catch {}

      try {
        if (localVideoRef.current) localVideoRef.current.srcObject = null;
        if (remoteVideoRef.current) remoteVideoRef.current.srcObject = null;
        if (remoteAudioRef.current) remoteAudioRef.current.srcObject = null;
      } catch {}

      stopAllMedia();

      try {
        pcRef.current?.close();
      } catch {}

      pcRef.current = null;
      localStreamRef.current = null;
      screenStreamRef.current = null;
      remoteStreamRef.current = new MediaStream();
      localVideoSenderRef.current = null;

      resetCallUiState();

      try {
        onClose();
      } catch (err) {
        console.error("cleanupCall onClose failed:", err);
      }
    },
    [callId, onClose, sharingScreen, target.peerId, video]
  );

  const createPeerConnection = () => {
    if (pcRef.current) return pcRef.current;

    const pc = new RTCPeerConnection(RTC_CONFIG);
    pcRef.current = pc;

    if (earlyIceRef.current.length) {
  pendingIceRef.current.push(...earlyIceRef.current);
  earlyIceRef.current = [];
}

    pc.onicecandidate = (e) => {
      if (e.candidate) {
        WailsApp.SendSignal(
          target.peerId,
          callId,
          "ice",
          JSON.stringify(e.candidate),
          video || sharingScreen
        ).catch((err) => {
          console.error("SendSignal ice failed:", err);
        });
      }
    };

    pc.ontrack = async (e) => {
      const incomingStream = e.streams?.[0];
      const track = e.track;

      if (incomingStream) {
        remoteStreamRef.current = incomingStream;
      } else {
        const exists = remoteStreamRef.current.getTracks().some((tr) => tr.id === track.id);
        if (!exists) remoteStreamRef.current.addTrack(track);
      }

      if (track.kind === "video") {
        track.onended = () => refreshRemoteVideoUi();
        track.onmute = () => refreshRemoteVideoUi();
        track.onunmute = () => refreshRemoteVideoUi();
      }

      await attachRemoteStream();
      await refreshRemoteVideoUi();
    };

    // FIX: убрана обработка "closed" здесь — pc.close() вызывается ИЗ
    // cleanupCall, так что дополнительный вызов cleanupCall из этого же
    // колбэка при переходе в closed приводил к путанице состояний между
    // двумя последовательными звонками. "failed"/"disconnected" остаются,
    // это реальные обрывы соединения по сети.
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === "connected") {
        setPhase("connected");
        clearCallError();
      }
      if (pc.connectionState === "failed" || pc.connectionState === "disconnected") {
        cleanupCall(false);
      }
    };

    return pc;
  };

  const renegotiateNow = async () => {
    const pc = pcRef.current;
    if (!pc || closedRef.current) return;
    if (makingOfferRef.current) return;
    if (pc.signalingState !== "stable") return;

    try {
      makingOfferRef.current = true;
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      await WailsApp.SendSignal(
        target.peerId,
        callId,
        "renegotiate-offer",
        JSON.stringify(offer),
        true
      );
    } catch (err) {
      console.warn("renegotiateNow failed:", err);
    } finally {
      makingOfferRef.current = false;
    }
  };

  const startLocalMedia = async () => {
    if (localStreamRef.current) return localStreamRef.current;
    if (!navigator.mediaDevices?.getUserMedia) {
      throw new Error("getUserMedia недоступен");
    }

    try { 
      console.log("requesting media...");
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true, video });
      console.log("media granted!", stream);
      localStreamRef.current = stream;

      if (video && localVideoRef.current) {
        localVideoRef.current.srcObject = stream;
        localVideoRef.current.muted = true;
        localVideoRef.current.play().catch(() => {});
      }

      return stream;
    } catch (err) {
      setErrorText(
        "Не удалось получить доступ к микрофону/камере. Проверь разрешения macOS для приложения."
      );
      throw err;
    }
  };

  const addLocalTracksToPeer = async (pc, stream) => {
    const existing = new Set(pc.getSenders().map((s) => s.track?.id).filter(Boolean));
    stream.getTracks().forEach((track) => {
      if (!existing.has(track.id)) {
        const sender = pc.addTrack(track, stream);
        if (track.kind === "video") localVideoSenderRef.current = sender;
      }
    });
  };

  const ensureVideoSender = async () => {
    const pc = pcRef.current;
    if (!pc) throw new Error("PeerConnection не создан");

    if (localVideoSenderRef.current) return localVideoSenderRef.current;

    let sender = pc.getSenders().find((s) => s.track && s.track.kind === "video");
    if (!sender) {
      const videoTransceiver = pc
        .getTransceivers()
        .find((tr) => tr.sender && (!tr.sender.track || tr.sender.track.kind === "video"));
      sender = videoTransceiver?.sender || null;
    }

    if (!sender) {
      const transceiver = pc.addTransceiver("video", { direction: "sendrecv" });
      sender = transceiver.sender;
    }

    localVideoSenderRef.current = sender;
    return sender;
  };

  const handleOfferLike = async (pc, payload) => {
    const offerCollision = pc.signalingState !== "stable";
    ignoreOfferRef.current = !politeRef.current && offerCollision;
    if (ignoreOfferRef.current) return;

    await pc.setRemoteDescription(JSON.parse(payload.data));

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

    if (payload.kind === "reject" || payload.kind === "end") {
      cleanupCall(false);
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
        await pc.setRemoteDescription(JSON.parse(payload.data));

        for (const cand of pendingIceRef.current) {
          try {
            await pc.addIceCandidate(JSON.parse(cand));
          } catch {}
        }
        pendingIceRef.current = [];

        await attachRemoteStream();
        await refreshRemoteVideoUi();
      } else if (payload.kind === "ice") {
        if (pc.remoteDescription) {
          try {
            await pc.addIceCandidate(JSON.parse(payload.data));
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
      } else if (payload.kind === "screen-share-stopped") {
        // FIX: раньше здесь только обновляли UI, но флаг remoteSharingScreen
        // мог "залипнуть" true, если трек ещё формально live, но уже
        // остановлен на стороне отправителя. Явный сброс + пересчёт.
        setRemoteSharingScreen(false);
        setRemoteZoomed(false);
        await refreshRemoteVideoUi();
      }
    } catch (err) {
      console.error("Signal handler failed:", err, payload);
      setErrorText("Ошибка обработки сигнала звонка.");
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
        await pc.setRemoteDescription(JSON.parse(incomingOffer));
        const answer = await pc.createAnswer();
        await pc.setLocalDescription(answer);
        await WailsApp.SendSignal(target.peerId, callId, "answer", JSON.stringify(answer), video);
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
      console.error("startConnection failed:", err);
      startedRef.current = false;
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

  const stopScreenShare = async ({ silent = false } = {}) => {
    const pc = pcRef.current;
    if (!pc) return;

    let sender = null;
    try {
      sender = await ensureVideoSender();
    } catch (err) {
      console.warn("ensureVideoSender failed during stop:", err);
    }

    const camTrack = localStreamRef.current?.getVideoTracks?.()[0] || null;

    try {
      if (sender) {
        if (camTrack) {
          await sender.replaceTrack(camTrack);
        } else {
          await sender.replaceTrack(null);
        }
      }
    } catch (err) {
      console.warn("stopScreenShare replaceTrack failed:", err);
    }

    try {
      screenStreamRef.current?.getTracks().forEach((tr) => {
        tr.onended = null;
        tr.stop();
      });
    } catch {}

    screenStreamRef.current = null;
    setSharingScreen(false);
    // FIX: раньше remoteSharingScreen/remoteZoomed не сбрасывались при
    // остановке СВОЕЙ демки — из-за этого UI второй стороны иногда "залипал"
    // в режиме демонстрации даже после её выключения.
    setRemoteZoomed(false);
    clearCallError();

    try {
      if (localVideoRef.current) {
        if (camTrack && localStreamRef.current) {
          localVideoRef.current.srcObject = localStreamRef.current;
          localVideoRef.current.muted = true;
          await localVideoRef.current.play().catch(() => {});
        } else {
          localVideoRef.current.pause?.();
          localVideoRef.current.srcObject = null;
        }
      }
    } catch {}

    await renegotiateNow();

    if (!silent) {
      WailsApp.SendSignal(target.peerId, callId, "screen-share-stopped", "", true).catch(() => {});
    }
  };

  const toggleScreenShare = async () => {
    const pc = pcRef.current;
    if (!pc) return;

    if (sharingScreen) {
      await stopScreenShare();
      return;
    }

    try {
      clearCallError();

      if (!navigator.mediaDevices?.getDisplayMedia) {
        throw new Error("getDisplayMedia недоступен");
      }

      const screenStream = await navigator.mediaDevices.getDisplayMedia({
        video: { displaySurface: "monitor", frameRate: 15 },
        audio: false,
      });

      const screenTrack = screenStream.getVideoTracks()[0];
      if (!screenTrack) throw new Error("Screen track не получен");

      screenStreamRef.current = screenStream;

      const sender = await ensureVideoSender();
      await sender.replaceTrack(screenTrack);

      if (localVideoRef.current) {
        localVideoRef.current.srcObject = screenStream;
        localVideoRef.current.muted = true;
        await localVideoRef.current.play().catch(() => {});
      }

      screenTrack.onended = async () => {
        await stopScreenShare({ silent: true });
      };

      setSharingScreen(true);
      await renegotiateNow();
    } catch (err) {
      console.warn("getDisplayMedia cancelled or failed:", err);
      setSharingScreen(false);
      // FIX: сбрасываем и remote-флаг — ошибка старта демки на своей
      // стороне не должна оставлять "фантомную" удалённую демку висящей.
      setRemoteSharingScreen(false);
      setRemoteZoomed(false);
      try {
        screenStreamRef.current?.getTracks().forEach((tr) => tr.stop());
      } catch {}
      screenStreamRef.current = null;
      setErrorText(
        "Не удалось начать демонстрацию экрана. Проверь разрешение Screen Recording и повтори."
      );
    }
  };

  const endCall = () => {
    cleanupCall(true, "end");
  };

  const fmt = (s) =>
    `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;

  const showRemoteVideoLayer = video || sharingScreen || remoteSharingScreen;
  const showSelfVideoLayer = video || sharingScreen;
  const isSharingAny = sharingScreen || remoteSharingScreen;

  return (
    <motion.div
      className="call-overlay"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
    >
      <motion.div
        className={"call-card " + (isSharingAny ? "sharing" : "")}
        initial={{ scale: 0.9, opacity: 0 }}
        animate={{ scale: 1, opacity: 1 }}
        exit={{ scale: 0.9, opacity: 0 }}
      >
        <audio ref={remoteAudioRef} autoPlay playsInline />

        {showRemoteVideoLayer && (
          <video
            ref={remoteVideoRef}
            autoPlay
            playsInline
            className={"call-remote-video " + (remoteZoomed ? "zoomed" : "")}
            onDoubleClick={() => setRemoteZoomed((z) => !z)}
            title="Двойной клик — увеличить/уменьшить"
          />
        )}

        {showSelfVideoLayer && (
          <video ref={localVideoRef} autoPlay playsInline muted className="call-self-video" />
        )}

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
              {showRemoteVideoLayer && <button type="button" className="call-btn">🎥</button>}
              <button
                type="button"
                className={"call-btn " + (sharingScreen ? "active" : "")}
                onClick={toggleScreenShare}
              >
                📺
              </button>
              <button type="button" className="call-btn" onClick={toggleMute}>
                {muted ? "🔇" : "🎙"}
              </button>
              <button type="button" className="call-btn end" onClick={endCall}>
                ✕
              </button>
            </div>
            <div className="call-labels">
              <span>
                {sharingScreen
                  ? "Демонстрация включена"
                  : remoteSharingScreen
                    ? "Удалённая демонстрация"
                    : t.call.screen}
              </span>
              <span>{muted ? t.call.unmute : t.call.mute}</span>
              <span>{t.call.end}</span>
            </div>
          </>
        )}
      </motion.div>
    </motion.div>
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
            <span className={"read-ticks " + (m.read ? "read" : "")}>
              {m.read ? "✓✓" : "✓"}
            </span>
          )}
        </span>
      </motion.div>

      {/* NEW: отображение поставленной реакции под бабблом */}
      {m.reaction && <div className="bubble-reaction">{m.reaction}</div>}

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
  t,
}) {
  const [text, setText] = useState("");
  const fileRef = useRef(null);
  const scrollRef = useRef(null);

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
    return () => {
      if (onTyping) onTyping(false);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chat?.id]);

  if (!chat) {
    return (
      <div className="main-panel glass">
        <div className="empty-hint" style={{ margin: "auto" }}>
          Выберите чат или найдите собеседника во вкладке "В сети"
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

  const attach = (e) => {
    const file = e.target.files?.[0];
    if (!file) return;

    const reader = new FileReader();
    reader.onload = () => {
      onSend({
        text: "",
        mediaKind: file.type.startsWith("video") ? "video" : "image",
        mediaData: reader.result,
      });
    };
    reader.readAsDataURL(file);
    e.target.value = "";
  };

  const displayMessages = (Array.isArray(messages) ? messages : []).map((m) => ({
    ...m,
    out: m.senderId === myPeerId,
  }));

  const typeLabel =
    chat.type === "saved"
      ? t.savedNotes
      : isPeerTyping
        ? "печатает…"
        : chat.online
          ? t.online
          : t.offline;

  return (
    <div className="main-panel glass">
      <div className="chat-header" onClick={() => onOpenProfile(chat, false)}>
        <Avatar name={chat.title} avatar={chat.avatar} online={chat.online} />
        <div className="chat-header-info">
          <div className="chat-header-name">{chat.title}</div>
          <div className={"chat-preview " + (isPeerTyping ? "typing-preview" : "")}>
            {blocked ? t.profile.blockedLabel : typeLabel}
          </div>
        </div>
        <ConnectionBadge status={connStatus} t={t} />
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
            <input
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
        <button
          type="button"
          className="theme-toggle"
          onClick={() => setTheme(theme === "light" ? "dark" : "light")}
        >
          {theme === "light" ? "☀ " + t.settings.light : "🌙 " + t.settings.dark}
        </button>
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
  const [theme, setTheme] = useState("dark");
  const [lang, setLang] = useState("ru");
  const t = useT(lang);

  const [profile, setProfileState] = useState(null);
  const [bootLoading, setBootLoading] = useState(true);
  const [chatsRaw, setChatsRaw] = useState([]);
  const [messagesByChat, setMessagesByChat] = useState({});
  const [blocked, setBlocked] = useState([]);

  // NEW: локальный чат "Избранное" — хранится в localStorage, не трогает бэкенд
  const [savedMessages, setSavedMessages] = useState(() => {
    try {
      const raw = localStorage.getItem(SAVED_STORAGE_KEY);
      return raw ? JSON.parse(raw) : [];
    } catch {
      return [];
    }
  });

  useEffect(() => {
    try {
      localStorage.setItem(SAVED_STORAGE_KEY, JSON.stringify(savedMessages));
    } catch {}
  }, [savedMessages]);

  // NEW: индикатор "печатает…" по каждому пиру
  const [typingByPeer, setTypingByPeer] = useState({});

  const [tab, setTab] = useState("all");
  const [search, setSearch] = useState("");
  const [activeChat, setActiveChat] = useState(null);
  const [showSettings, setShowSettings] = useState(false);
  const [connStatus, setConnStatus] = useState("connecting");
  const [onlinePeersRaw, setOnlinePeersRaw] = useState([]);
  const [profileTarget, setProfileTarget] = useState(null);
  const [profileIsMe, setProfileIsMe] = useState(false);
  const [mediaChatId, setMediaChatId] = useState(null);
  const [callState, setCallState] = useState(null);
  // NEW: полноэкранный просмотр медиа
  const [viewerItem, setViewerItem] = useState(null);

  const signalHandlersRef = useRef(new Set());
  const blockedRef = useRef(blocked);
  const chatsRawRef = useRef(chatsRaw);
  const onlinePeersRawRef = useRef(onlinePeersRaw);
  const activeCallIdRef = useRef(null);
  const typingSendTimerRef = useRef(null);

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

  const registerSignalHandler = useCallback((handler) => {
    signalHandlersRef.current.add(handler);
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

    return () => {
      clearTimeout(t1);
      clearTimeout(t2);
      clearTimeout(t3);
    };
  }, [profile?.peerId, refreshChats, refreshOnlinePeers]);

  useEffect(() => {
    if (!profile) return;

    setConnStatus("connected");

    const cancelPeers = EventsOn("peers:update", (peers) => {
      setOnlinePeersRaw(Array.isArray(peers) ? peers : []);
    });

    const cancelIncoming = EventsOn("message:incoming", (msg = {}) => {
      if (!msg.chatId) return;
      setMessagesByChat((prev) => ({
        ...prev,
        [msg.chatId]: [...(prev[msg.chatId] || []), msg],
      }));
      refreshChats();
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

    // NEW: реакция от собеседника
    const cancelReacted = EventsOn("message:reacted", ({ chatId, id, reaction } = {}) => {
      if (!chatId || !id) return;
      setMessagesByChat((prev) => ({
        ...prev,
        [chatId]: (prev[chatId] || []).map((m) =>
          m.id === id ? { ...m, reaction } : m
        ),
      }));
    });

    const cancelProfileUpdated = EventsOn("profile:updated", () => refreshChats());
    const cancelAccountDeleted = EventsOn("account:deleted", () => refreshChats());

    const onSignalIncoming = (payload = {}) => {
      // NEW: индикатор "печатает…" приходит по этому же каналу, но не должен
      // попадать в логику звонков ниже.
      if (payload.kind === "typing") {
        try {
          const data = JSON.parse(payload.data || "{}");
          setTypingByPeer((prev) => ({
            ...prev,
            [payload.peerId]: !!data.isTyping,
          }));
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
            },
            video: payload.video === true,
            isCaller: false,
            callId: payload.callId,
            incomingOffer: payload.data,
          };
        });
      }

      signalHandlersRef.current.forEach((h) => h(payload));
    };

    const cancelSignal = EventsOn("signal:incoming", onSignalIncoming);

    const netCheck = () => {
      if (typeof navigator !== "undefined" && "onLine" in navigator) {
        setConnStatus(navigator.onLine ? "connected" : "reconnecting");
        if (!navigator.onLine) setOnlinePeersRaw([]);
        else refreshOnlinePeers();
      }
    };

    window.addEventListener("offline", netCheck);
    window.addEventListener("online", netCheck);

    return () => {
      cancelPeers?.();
      cancelIncoming?.();
      cancelDeleted?.();
      cancelRead?.();
      cancelReacted?.();
      cancelProfileUpdated?.();
      cancelAccountDeleted?.();
      cancelSignal?.();
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
      alert("Не удалось открыть чат: " + (err?.message || err));
    }
  };

  const sendMessage = async (chatId, { text, mediaKind, mediaData }) => {
    try {
      const msg = await WailsApp.SendMessage(chatId, text, mediaKind, mediaData);
      setMessagesByChat((prev) => ({
        ...prev,
        [chatId]: [...(prev[chatId] || []), msg],
      }));
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

  // NEW: отправка сигнала "печатает…" с дебаунсом, чтобы не спамить сеть
  const handleTyping = useCallback(
    (peerId, isTyping) => {
      if (!peerId || peerId === SAVED_CHAT_ID) return;
      if (typingSendTimerRef.current) clearTimeout(typingSendTimerRef.current);
      typingSendTimerRef.current = setTimeout(() => {
        WailsApp.SendTyping(peerId, isTyping).catch((err) => {
          console.error("SendTyping failed:", err);
        });
      }, 120);
    },
    []
  );

  const startCall = (target, video) => {
    if (callState) return;

    const callId = "call-" + Date.now() + "-" + Math.random().toString(36).slice(2, 8);
    setCallState({
      target: {
        peerId: target.peerId,
        name: target.name || target.title || target.peerId,
        title: target.title || target.name || target.peerId,
        avatar: target.avatar || "",
      },
      video,
      isCaller: true,
      callId,
      incomingOffer: null,
    });
    setProfileTarget(null);
  };

  const closeCall = useCallback(() => {
    setCallState(null);
  }, []);

  const logout = async () => {
    try {
      await WailsApp.Logout();
    } catch (err) {
      console.error("Logout failed:", err);
    }
    setProfileState(null);
    setChatsRaw([]);
    setMessagesByChat({});
    setBlocked([]);
    setOnlinePeersRaw([]);
    setActiveChat(null);
    setShowSettings(false);
    setProfileTarget(null);
    setMediaChatId(null);
    setCallState(null);
    setTypingByPeer({});
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

  if (bootLoading) return <div className="app-root" />;
  if (!profile) return <Onboarding onDone={(p) => setProfileState(p)} />;

  return (
    <div className="app-root">
      <div className="titlebar" />
      <div className="app-shell">
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
        />

        {showSettings ? (
          <SettingsPanel
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

        {viewerItem && (
          <MediaViewer item={viewerItem} onClose={() => setViewerItem(null)} />
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