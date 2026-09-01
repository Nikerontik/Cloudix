/*
 * Cloudix mobile bridge.
 *
 * Wails is a desktop-only runtime, so on iOS/Android there is no window.go and
 * no window.runtime. This file installs stand-ins for both before the app
 * bundle loads, which is what lets frontend/wailsjs/** and App.jsx run
 * unmodified: they still call window.go.app.App.X() and window.runtime.EventsOn,
 * they just reach a WKWebView message handler or an Android JavascriptInterface
 * instead of the Wails IPC.
 *
 * On desktop this file detects no native handler and returns immediately,
 * leaving the real Wails runtime alone.
 */
(function () {
  "use strict";

  var iosHandler =
    window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.cloudix;
  var androidHandler = window.CloudixNative;
  if (!iosHandler && !androidHandler) return; // desktop — Wails owns these globals

  var boot = window.__cloudixBoot || {};
  var platform = boot.platform || (iosHandler ? "ios" : "android");
  var features = boot.features || {};

  var seq = 0;
  var pending = Object.create(null);

  function post(msg) {
    if (iosHandler) iosHandler.postMessage(msg);
    else androidHandler.post(JSON.stringify(msg));
  }

  function call(method, args) {
    return new Promise(function (resolve, reject) {
      var id = ++seq;
      pending[id] = { resolve: resolve, reject: reject };
      try {
        post({ type: "call", id: id, method: method, args: JSON.stringify(args) });
      } catch (e) {
        delete pending[id];
        reject(e);
      }
    });
  }

  /* ---- native -> js ------------------------------------------------------ */

  var listeners = Object.create(null);

  window.__cloudix = {
    // resolve(id, ok, payload): payload is the method's JSON result, or the
    // error message when ok is false. An empty result means "no value".
    resolve: function (id, ok, payload) {
      var p = pending[id];
      if (!p) return;
      delete pending[id];
      if (!ok) {
        p.reject(new Error(payload || "call failed"));
        return;
      }
      if (payload === "" || payload == null) {
        p.resolve(undefined);
        return;
      }
      try {
        p.resolve(JSON.parse(payload));
      } catch (e) {
        p.resolve(payload);
      }
    },

    event: function (name, payloadJSON) {
      var handlers = listeners[name];
      if (!handlers || !handlers.length) return;
      var data;
      try {
        data = payloadJSON === "" || payloadJSON == null ? undefined : JSON.parse(payloadJSON);
      } catch (e) {
        data = payloadJSON;
      }
      // Wails hands listeners the payload as the first argument.
      handlers.slice().forEach(function (fn) {
        try {
          fn(data);
        } catch (e) {
          console.error("event handler for " + name + " failed:", e);
        }
      });
    },

    // The shell calls this when the OS reports connectivity changed, so the
    // overlay can rebuild its session the same way the desktop app does.
    networkChanged: function () {
      call("RestartNetworking", []).catch(function () {});
    },

    platform: platform,
    features: features,
  };

  /* ---- window.go --------------------------------------------------------- */

  var METHODS = [
    "AddManualPeer", "AppVersion", "BlockPeer", "DeleteChat", "DeleteMessage",
    "GetChats", "GetDataDir", "GetMessages", "GetOnlinePeers", "GetProfile",
    "ListBlocked", "Logout", "MarkChatRead", "NetworkReady", "OpenDataFolder",
    "ReactToMessage", "Register", "RemoveManualPeer", "RestartNetworking",
    "SaveMedia", "SendMessage", "SendPing", "SendSignal", "SendTyping",
    "StartChatWithPeer", "UnblockPeer", "UpdateProfile", "VPNCreate", "VPNJoin",
    "VPNJoinByInvite", "VPNLeave", "VPNStatus",
  ];

  var App = {};
  METHODS.forEach(function (name) {
    App[name] = function () {
      return call(name, Array.prototype.slice.call(arguments));
    };
  });
  window.go = { app: { App: App } };

  /* ---- window.runtime ---------------------------------------------------- */

  function on(name, fn) {
    (listeners[name] || (listeners[name] = [])).push(fn);
    return function () {
      var arr = listeners[name];
      if (!arr) return;
      var i = arr.indexOf(fn);
      if (i >= 0) arr.splice(i, 1);
    };
  }

  var noop = function () {};
  var resolved = function (v) {
    return function () {
      return Promise.resolve(v);
    };
  };

  window.runtime = {
    EventsOn: on,
    EventsOnMultiple: function (name, fn) {
      return on(name, fn);
    },
    EventsOnce: function (name, fn) {
      var off = on(name, function (d) {
        off();
        fn(d);
      });
      return off;
    },
    EventsOff: function (name) {
      delete listeners[name];
    },
    EventsOffAll: function () {
      listeners = Object.create(null);
    },
    EventsEmit: function (name) {
      // Frontend-only events stay in the page; nothing on the Go side listens.
      window.__cloudix.event(name, JSON.stringify(arguments[1]));
    },

    Environment: resolved({ buildType: "production", platform: platform, arch: "" }),

    BrowserOpenURL: function (url) {
      post({ type: "openURL", url: String(url) });
    },

    // There is no window to manage on a phone. These are called by the desktop
    // title bar, which mobile never renders, but the imports must resolve.
    WindowMinimise: noop,
    WindowToggleMaximise: noop,
    WindowUnmaximise: noop,
    WindowMaximise: noop,
    WindowIsMaximised: resolved(false),
    WindowIsMinimised: resolved(false),
    WindowIsFullscreen: resolved(false),
    WindowFullscreen: noop,
    WindowUnfullscreen: noop,
    WindowHide: noop,
    WindowShow: noop,
    WindowReload: function () {
      window.location.reload();
    },
    WindowSetTitle: noop,
    Quit: noop,
    Hide: noop,
    Show: noop,

    ClipboardGetText: function () {
      if (navigator.clipboard && navigator.clipboard.readText) return navigator.clipboard.readText();
      return Promise.resolve("");
    },
    ClipboardSetText: function (text) {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        return navigator.clipboard.writeText(text).then(function () {
          return true;
        });
      }
      return Promise.resolve(false);
    },

    LogPrint: noop, LogTrace: noop, LogDebug: noop, LogInfo: noop,
    LogWarning: noop, LogError: noop, LogFatal: noop,
  };
})();
