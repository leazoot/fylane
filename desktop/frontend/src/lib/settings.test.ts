import { describe, expect, it } from "vitest";
import {
  awaitingHostname,
  canUse,
  codeLeft,
  connectionLive,
  readSettings,
  stepTimeout,
  updateNotice,
  TIMEOUTS,
  type SettingsReaders,
} from "./settings";
import { DICT } from "./i18n";
import type { CoreStatusInfo, PrefsInfo, TunnelProvider } from "./core";

const WAY: TunnelProvider = {
  kind: "cloudflare-named",
  binary: "cloudflared",
  install: "brew install cloudflared",
  installed: true,
  needs_token: false,
  needs_hostname: true,
  stable: true,
  setup: "browser",
  authorized: false,
  checkable: true,
  can_sign_out: true,
  opens_browser: true,
};

const PREFS: PrefsInfo = {
  task_timeout_seconds: 60,
  allow_stop_tasks: true,
  autostart: { supported: true, enabled: false },
  read_boundary: { state: "enforced", detail: "subprocess reads are bounded" },
};

const STATUS: CoreStatusInfo = {
  version: "0.0.1",
  pending_approvals: 0,
  tunnel: "connected",
  approval_mode: "safe",
};

function readers(over: Partial<SettingsReaders> = {}): SettingsReaders {
  return {
    prefs: async () => PREFS,
    commands: async () => ({ rung: "workspace" as const, grants: [] }),
    status: async () => STATUS,
    ...over,
  };
}

describe("readSettings", () => {
  it("carries the forwarded MCP providers through, and an older Core through as none", async () => {
    // The gateway's providers are an authorization in force, so the
    // page has to be able to draw them. A Core that predates the field must
    // read as an empty list rather than as undefined — a page that crashes on
    // an older Core is not a page that keeps anything visible.
    const withProxies = await readSettings(
      readers({
        commands: async () => ({
          rung: "workspace" as const,
          grants: [],
          providers: [{ name: "sqlite", trust: "ask" }],
        }),
      }),
    );
    expect(withProxies.proxies).toEqual([{ name: "sqlite", trust: "ask" }]);

    const older = await readSettings(readers());
    expect(older.proxies).toEqual([]);

    // A gate that did not answer leaves nothing to show rather than a
    // half-truth about what is configured.
    const failed = await readSettings(
      readers({
        commands: async () => {
          throw new Error("no gate");
        },
      }),
    );
    expect(failed.proxies).toEqual([]);
  });

  it("reads the language servers the same three ways", async () => {
    // Same field, same three answers, and the empty one matters most: a Core
    // that predates it must read as "none installed", not as undefined.
    const withServers = await readSettings(
      readers({
        commands: async () => ({
          rung: "workspace" as const,
          grants: [],
          language_servers: [{ name: "gopls", extensions: [".go"], running: true }],
        }),
      }),
    );
    expect(withServers.servers).toEqual([
      { name: "gopls", extensions: [".go"], running: true },
    ]);

    expect((await readSettings(readers())).servers).toEqual([]);

    const failed = await readSettings(
      readers({
        commands: async () => {
          throw new Error("no gate");
        },
      }),
    );
    expect(failed.servers).toEqual([]);
  });

  it("reports a failed read as a failed read, not as a failed change", async () => {
    // Regression: opening the page against a Core that predates
    // /v1/settings answered 404, and the page said "could not change the
    // setting" — an edit the user never made. What failed was the read.
    const res = await readSettings(
      readers({
        prefs: async () => {
          throw new Error("404");
        },
      }),
    );
    expect(res.errors).toEqual(["shell.errPrefsRead"]);
    expect(res.errors).not.toContain("shell.errPrefs");
    expect(DICT["shell.errPrefsRead"].en).not.toBe(DICT["shell.errPrefs"].en);
    expect(DICT["shell.errPrefsRead"].zh).not.toBe(DICT["shell.errPrefs"].zh);
  });

  it("leaves prefs null so the rows it feeds can be shown as unavailable", async () => {
    const res = await readSettings(
      readers({
        prefs: async () => {
          throw new Error("404");
        },
      }),
    );
    // Not a zero-valued object: 0 seconds is not a stored timeout, and a row
    // that renders one invites a click that can only fail again.
    expect(res.prefs).toBeNull();
  });

  it("shows the half that answered when the other half did not", async () => {
    const res = await readSettings(
      readers({
        commands: async () => {
          throw new Error("no gate");
        },
      }),
    );
    expect(res.prefs).toEqual(PREFS);
    expect(res.rung).toBeNull();
    expect(res.errors).toEqual(["shell.errGateRead"]);
  });

  it("raises both messages when the Core is not answering at all", async () => {
    const down = async () => {
      throw new Error("core is not running");
    };
    const res = await readSettings({ prefs: down, commands: down, status: down });
    expect(res.errors).toEqual(["shell.errPrefsRead", "shell.errGateRead"]);
  });

  it("says nothing when both halves answered", async () => {
    const res = await readSettings(readers());
    expect(res.errors).toEqual([]);
    expect(res.prefs).toEqual(PREFS);
    expect(res.rung).toBe("workspace");
  });

  it("carries the write mode, which rides on the status answer", async () => {
    const res = await readSettings(readers({ status: async () => ({ ...STATUS, approval_mode: "balanced" }) }));
    expect(res.mode).toBe("balanced");
  });

  it("leaves the write mode null and says so when only the status read failed", async () => {
    // The two approval axes are read from two endpoints, so one can answer
    // without the other. A null mode must not fall back to "safe": that is
    // the default, so a guess would land on it and look right while the
    // machine was running the other one.
    const res = await readSettings({
      ...readers(),
      status: async () => {
        throw new Error("no status");
      },
    });
    expect(res.mode).toBeNull();
    expect(res.rung).toBe("workspace");
    expect(res.errors).toEqual(["shell.errGateRead"]);
  });
});

describe("canUse", () => {
  it("refuses a way in whose program is not on the machine", () => {
    expect(canUse({ ...WAY, installed: false, authorized: true })).toBe(false);
  });

  it("lets a way in with nothing to authorize straight through", () => {
    expect(
      canUse({
        ...WAY,
        setup: "none",
        checkable: false,
        needs_hostname: false,
      }),
    ).toBe(true);
  });

  it("waits for the browser sign-in before offering to start", () => {
    expect(canUse(WAY, "", "fylane.example.com")).toBe(false);
    expect(canUse({ ...WAY, authorized: true }, "", "fylane.example.com")).toBe(true);
  });

  it("gives the benefit of the doubt when the sign-in state cannot be read", () => {
    // Tailscale keeps this in a daemon Fylane cannot see. Reading "cannot
    // tell" as "not signed in" would refuse to start a tunnel that works.
    const tailscale = { ...WAY, kind: "tailscale-funnel", checkable: false, needs_hostname: false };
    expect(canUse(tailscale)).toBe(true);
  });

  it("counts a token typed just now, before the Core has stored it", () => {
    const ngrok: TunnelProvider = {
      ...WAY,
      kind: "ngrok",
      setup: "token",
      needs_token: true,
      needs_hostname: false,
    };
    expect(canUse(ngrok, "   ")).toBe(false);
    expect(canUse(ngrok, "2abc")).toBe(true);
  });

  it("refuses a named tunnel with nowhere to answer", () => {
    // "Use this" used to be offered with the hostname field empty. Pressing it
    // asked the Core to restart into a tunnel that cannot resolve, and the
    // failure arrived after the restart rather than before it.
    const signedIn = { ...WAY, authorized: true };
    expect(canUse(signedIn, "", "")).toBe(false);
    expect(canUse(signedIn, "", "   ")).toBe(false);
    expect(canUse(signedIn, "", "fylane.example.com")).toBe(true);
  });
});

describe("awaitingHostname", () => {
  // The domain is read out of the sign-in's certificate and then looked up
  // over the network, so it lands a few polls after the sign-in does.
  const signedIn = { ...WAY, authorized: true };

  it("is waiting while signed in with no suggestion and an untouched field", () => {
    expect(awaitingHostname(signedIn, "", "")).toBe(true);
  });

  it("stops waiting once the suggestion arrives", () => {
    expect(awaitingHostname(signedIn, "fylane.example.com", "")).toBe(false);
  });

  it("stops waiting the moment the user types their own", () => {
    // Someone who knows their hostname should not be held up by a lookup.
    expect(awaitingHostname(signedIn, "", "mine.example.com")).toBe(false);
  });

  it("is not waiting when nothing is coming", () => {
    // Not signed in: an empty field means "type one", not "hold on".
    expect(awaitingHostname(WAY, "", "")).toBe(false);
    expect(awaitingHostname({ ...signedIn, needs_hostname: false }, "", "")).toBe(false);
  });
});

describe("readSettings, continued", () => {
  it("still says nothing when both halves answered", async () => {
    const res = await readSettings(readers());
    expect(res.errors).toEqual([]);
    expect(res.prefs).toEqual(PREFS);
    expect(res.rung).toBe("workspace");
  });
});

describe("stepTimeout", () => {
  // The real machine had 50 seconds stored — written by an older build, or by
  // hand. The stepper has to behave from there, not only from its own values.
  it("steps to the neighbour on the side that was pressed, not to the closest value", () => {
    // 45 is between two offered values. Snapping to the nearest entry first
    // would treat it as 50 and send "+" past 50 in a single press.
    expect(stepTimeout(45, 1)).toBe(50);
    expect(stepTimeout(45, -1)).toBe(30);
  });

  it("walks its own values one at a time", () => {
    // Neighbour by neighbour, in both directions, whatever the list holds —
    // written against TIMEOUTS rather than against literals so that changing
    // the offered set cannot quietly turn this into a test of nothing.
    for (let i = 0; i < TIMEOUTS.length - 1; i++) {
      expect(stepTimeout(TIMEOUTS[i], 1)).toBe(TIMEOUTS[i + 1]);
      expect(stepTimeout(TIMEOUTS[i + 1], -1)).toBe(TIMEOUTS[i]);
    }
  });

  it("answers null at each end, which is what disables the button", () => {
    expect(stepTimeout(TIMEOUTS[0], -1)).toBeNull();
    expect(stepTimeout(TIMEOUTS[TIMEOUTS.length - 1], 1)).toBeNull();
  });

  it("still moves from a value outside the range in both directions", () => {
    expect(stepTimeout(5, 1)).toBe(30);
    expect(stepTimeout(5, -1)).toBeNull();
    expect(stepTimeout(9000, -1)).toBe(300);
    expect(stepTimeout(9000, 1)).toBeNull();
  });
});

describe("connectionLive", () => {
  // The real machine reported mode "relay", state "" and no address, because
  // the relay pairing had been removed. The panel still drew a sage dot and
  // "in effect" — a green light on a lane nothing could reach.
  it("does not call relay mode a connection when there is no address", () => {
    expect(connectionLive({ mode: "relay", state: "" })).toBe(false);
    expect(connectionLive({ mode: "relay", state: "", connector_url: "" })).toBe(false);
  });

  it("calls relay a connection once there is an address to reach this machine on", () => {
    expect(connectionLive({ mode: "relay", state: "", connector_url: "https://relay/c/x" })).toBe(
      true,
    );
  });

  it("counts a tunnel that is coming up, because its address follows a moment later", () => {
    expect(connectionLive({ mode: "direct", state: "starting" })).toBe(true);
    expect(connectionLive({ mode: "direct", state: "running" })).toBe(true);
  });

  it("does not count a tunnel that stopped or failed", () => {
    expect(connectionLive({ mode: "direct", state: "stopped" })).toBe(false);
    expect(connectionLive({ mode: "direct", state: "failed" })).toBe(false);
  });
});

describe("how long a pairing code has left", () => {
  it("reads as minutes and seconds while there is a minute to read", () => {
    expect(codeLeft(600)).toBe("10:00");
    expect(codeLeft(597)).toBe("9:57");
    expect(codeLeft(61)).toBe("1:01");
  });

  it("drops to bare seconds under a minute, where a colon buys nothing", () => {
    expect(codeLeft(59)).toBe("59");
    expect(codeLeft(1)).toBe("1");
  });

  it("returns nothing at zero rather than a clock stuck at 0:00", () => {
    // A code at zero is gone. Showing 0:00 beside it says it is about to be.
    expect(codeLeft(0)).toBeNull();
    expect(codeLeft(-4)).toBeNull();
  });
});

// The Core has run the update check daily and reported it on /v1/status since
// nothing on this side declared the fields, so the notice half never
// happened. What is pinned is which absences mean "say nothing" — and that
// none of them is licence to say the machine is current.
describe("updateNotice", () => {
  const st = (over: Partial<CoreStatusInfo> = {}): CoreStatusInfo => ({ ...STATUS, ...over });

  it("names the release and the page when there is one", () => {
    expect(
      updateNotice(
        st({ update_available: true, latest_version: "0.0.2", download_page: "https://x.invalid" }),
      ),
    ).toEqual({ version: "0.0.2", page: "https://x.invalid" });
  });

  it("keeps the notice and drops the link while there is no release page", () => {
    expect(updateNotice(st({ update_available: true, latest_version: "0.0.2" }))).toEqual({
      version: "0.0.2",
      page: undefined,
    });
  });

  it("says nothing when the check found nothing", () => {
    expect(updateNotice(st({ update_available: false, latest_version: "0.0.1" }))).toBeUndefined();
  });

  // Both fields missing is the default build: the check is off, so nobody
  // looked. It reads the same as "nothing newer" on screen and must never
  // read as "you are up to date".
  it("says nothing when the check is switched off", () => {
    expect(updateNotice(st())).toBeUndefined();
  });

  // A version it cannot name is a notice with nothing to say.
  it("says nothing when a newer version is claimed but not named", () => {
    expect(updateNotice(st({ update_available: true }))).toBeUndefined();
  });

  it("says nothing when the Core could not be reached at all", () => {
    expect(updateNotice(null)).toBeUndefined();
  });
});
