// Appearance is three states, not a boolean: Auto follows the system and is
// the default, and the two explicit choices exist for the case the system
// setting cannot express — a light window in a dark room, or the reverse.
// The choice is stamped on <html> as data-fy; style.css carries the tokens.

export type Theme = "auto" | "light" | "dark";

const STORAGE_KEY = "fylane.theme";

export function storedTheme(): Theme {
  try {
    const saved = window.localStorage.getItem(STORAGE_KEY);
    if (saved === "auto" || saved === "light" || saved === "dark") {
      return saved;
    }
  } catch {
    // Private mode or a locked-down profile: follow the system.
  }
  return "auto";
}

export function storeTheme(theme: Theme): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // Not being able to remember the choice is not a reason to refuse it.
  }
}

/** applyTheme stamps the root element. Auto removes the attribute entirely
 * so the media query — and nothing else — decides. */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  if (theme === "auto") {
    root.removeAttribute("data-fy");
    return;
  }
  root.setAttribute("data-fy", theme);
}

// Density is a second axis of the same kind: a display preference this
// machine remembers, with no bearing on what the Core does. It lives here
// rather than in a new file because the storage rules are identical — a
// browser that refuses to remember must not refuse the choice.

export type Density = "comfortable" | "compact";

const DENSITY_KEY = "fylane.density";

export function storedDensity(): Density {
  try {
    if (window.localStorage.getItem(DENSITY_KEY) === "compact") {
      return "compact";
    }
  } catch {
    // Private mode or a locked-down profile: the roomy list is the default.
  }
  return "comfortable";
}

export function storeDensity(density: Density): void {
  try {
    window.localStorage.setItem(DENSITY_KEY, density);
  } catch {
    // Same as the theme: forgetting is allowed, refusing is not.
  }
}

// Which machine the lane's rail stands on, remembered the same way. "" is
// this computer. A remembered id that no longer exists is dropped by the
// window on its first poll, so a stale value can never point at nothing.
const MACHINE_KEY = "fylane.machine";

export function storedMachine(): string {
  try {
    return window.localStorage.getItem(MACHINE_KEY) ?? "";
  } catch {
    return "";
  }
}

export function storeMachine(id: string): void {
  try {
    if (id) window.localStorage.setItem(MACHINE_KEY, id);
    else window.localStorage.removeItem(MACHINE_KEY);
  } catch {
    // Forgetting is allowed, refusing is not.
  }
}
