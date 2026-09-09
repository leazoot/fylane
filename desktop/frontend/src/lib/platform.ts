// Which window decoration to draw. Desktop v2 §2 gives macOS and Windows the
// same content layout and different chrome: macOS keeps its own traffic
// lights and the content reserves room for them, Windows gets an app mark on
// the left and caption buttons on the right.
//
// Read from the user agent rather than asked of the Wails runtime: the answer
// is needed on the very first paint, before any async call can come back, and
// a title bar that changes shape one frame in is worse than one that is right
// immediately.

export type OS = "macos" | "windows";

export function detectOS(agent: string = navigator.userAgent): OS {
  return /Windows|Win64|WOW64/i.test(agent) ? "windows" : "macos";
}

/** Stamps the root element so CSS can pick the platform's font stack. */
export function applyOS(os: OS): void {
  document.documentElement.setAttribute("data-os", os);
}
