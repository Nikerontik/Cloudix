// Which platform the UI runs on, and what that platform can actually do.
//
// The rule for mobile is: a capability the build cannot deliver is *absent*
// from the UI, not present and inert. A share button that always fails or a
// LAN tab that can never populate reads as a broken app, not a limited one.
//
// On desktop no bridge is installed, so everything stays on and the Wails build
// behaves exactly as it did before.

const bridge = typeof window !== "undefined" ? window.__cloudix : undefined;

export const nativePlatform = (bridge && bridge.platform) || "";
export const isMobile = nativePlatform === "ios" || nativePlatform === "android";
export const isIOS = nativePlatform === "ios";
export const isAndroid = nativePlatform === "android";

const DESKTOP = {
  calls: true,
  screenShareSend: true,
  screenShareReceive: true,
  lanDiscovery: true,
  manualPeers: true,
  networkHosting: true,
  backgroundDelivery: true,
  openDataFolder: true,
  notifications: true,
};

// Anything the shell does not explicitly enable is off on mobile: a new
// capability should have to be switched on deliberately per platform.
const MOBILE_OFF = Object.keys(DESKTOP).reduce((acc, k) => ((acc[k] = false), acc), {});

export const features = isMobile
  ? { ...MOBILE_OFF, ...((bridge && bridge.features) || {}) }
  : DESKTOP;

export const can = (name) => features[name] === true;
