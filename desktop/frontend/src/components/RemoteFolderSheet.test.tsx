// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { LangContext } from "../lib/i18n";
import type { MachineInfo, RemoteListing } from "../lib/core";
import { RemoteFolderSheet } from "./RemoteFolderSheet";

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  vi.useFakeTimers();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

const MACHINE: MachineInfo = {
  id: "m_vps1",
  name: "vps-1",
  host: "vps.example.com",
  user: "deploy",
  state: "online",
  since: "2026-09-12T08:00:00Z",
};

// A small tree, answered the way the Core does: resolved path, parent,
// home, and only directories.
const HOME = "/home/deploy";
const TREE: Record<string, RemoteListing["entries"]> = {
  "/": [{ name: "home" }, { name: "srv" }],
  "/home": [{ name: "deploy" }],
  [HOME]: [
    { name: ".config", hidden: true },
    { name: "app", repo: true },
    { name: "notes" },
  ],
  [HOME + "/app"]: [{ name: "src" }],
  [HOME + "/app/src"]: [],
  [HOME + "/notes"]: [],
  "/srv": [],
};

function fakeBrowse(asked: string[]) {
  return async (path: string): Promise<RemoteListing> => {
    asked.push(path);
    let p = path === "" ? HOME : path;
    if (p === "~") p = HOME;
    if (p.startsWith("~/")) p = HOME + p.slice(1);
    const entries = TREE[p];
    if (!entries) return { entries: [], reason: "nodir" };
    return {
      path: p,
      parent: p === "/" ? undefined : p.slice(0, p.lastIndexOf("/")) || "/",
      home: HOME,
      entries,
    };
  };
}

function draw(node: React.ReactNode) {
  act(() => {
    root.render(
      <LangContext.Provider value={{ lang: "en", setLang: () => {} }}>
        {node}
      </LangContext.Provider>,
    );
  });
}

// Promises settle on microtasks; fake timers do not touch those.
const settle = () => act(async () => {});

function type(value: string) {
  const el = host.querySelector("#fy-folder-path") as HTMLInputElement;
  act(() => {
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const field = () =>
  (host.querySelector("#fy-folder-path") as HTMLInputElement).value;
const standing = () => host.querySelector(".fy-sheet-path-now")?.textContent;
const fieldOpen = () =>
  host.querySelector(".fy-sheet-path-field")?.getAttribute("data-open");
const rows = () =>
  Array.from(host.querySelectorAll(".fy-dir")).map((r) => r.textContent);
const status = () => host.querySelector("[role=status]")?.textContent ?? "";
const grant = () =>
  host.querySelector(".fy-sheet-approve") as HTMLButtonElement;
const click = (label: string) => {
  const b = Array.from(host.querySelectorAll("button")).find(
    (x) =>
      x.textContent === label ||
      x.querySelector(".fy-wsitem-name")?.textContent === label,
  );
  if (!b) throw new Error(`no button ${label}`);
  act(() => b.click());
};

describe("the remote folder sheet", () => {
  it("opens in the machine's home and steps into folders", async () => {
    const asked: string[] = [];
    const granted: string[] = [];
    draw(
      <RemoteFolderSheet
        machine={MACHINE}
        browse={fakeBrowse(asked)}
        onSubmit={async (p) => {
          granted.push(p);
        }}
        onCancel={() => {}}
      />,
    );
    expect(status()).toContain("Asking the machine");
    expect(grant().disabled).toBe(true);
    await settle();
    expect(asked).toEqual([""]);
    expect(standing()).toBe(HOME);
    expect(field()).toBe(HOME);
    // The field stays folded until asked for.
    expect(fieldOpen()).toBe("false");
    // Dot folders stay out of the way until asked for; the repo is marked.
    expect(rows()).toEqual(["Up one level..", "appgit", "notes"]);
    expect(status()).toContain("3 folders inside");
    expect(grant().disabled).toBe(false);

    click("show 1 hidden");
    expect(rows()).toEqual(["Up one level..", ".config", "appgit", "notes"]);

    click("app");
    await settle();
    expect(field()).toBe(HOME + "/app");
    expect(rows()).toEqual(["Up one level..", "src"]);
    // The way back: one level, and home from anywhere.
    click("Up one level..");
    await settle();
    expect(field()).toBe(HOME);
    const verbs = () =>
      Array.from(host.querySelectorAll(".fy-textbtn")).map(
        (b) => b.textContent,
      );
    expect(verbs()).not.toContain("home");
    click("Up one level..");
    await settle();
    expect(field()).toBe("/home");
    expect(verbs()).toContain("home");
    click("home");
    await settle();
    expect(field()).toBe(HOME);

    click("notes");
    await settle();
    expect(status()).toContain("Nothing inside");
    act(() => {
      grant().form?.requestSubmit();
    });
    await settle();
    expect(granted).toEqual([HOME + "/notes"]);
  });

  it("confirms a typed path with the machine before it can be granted", async () => {
    const asked: string[] = [];
    const granted: string[] = [];
    draw(
      <RemoteFolderSheet
        machine={MACHINE}
        browse={fakeBrowse(asked)}
        onSubmit={async (p) => {
          granted.push(p);
        }}
        onCancel={() => {}}
      />,
    );
    await settle();
    click("type a path");
    act(() => vi.advanceTimersByTime(1));
    expect(fieldOpen()).toBe("true");
    expect(document.activeElement?.id).toBe("fy-folder-path");
    type("~/ap");
    expect(grant().disabled).toBe(true);
    expect(status()).toContain("Checking");
    act(() => vi.advanceTimersByTime(700));
    await settle();
    expect(status()).toContain("no such folder");
    expect(grant().disabled).toBe(true);
    expect(rows()).toEqual([]);

    type("~/app");
    act(() => vi.advanceTimersByTime(700));
    await settle();
    // What was typed stays as typed; what is granted is what the machine
    // resolved it to.
    expect(field()).toBe("~/app");
    expect(standing()).toBe(HOME + "/app");
    expect(rows()).toEqual(["Up one level..", "src"]);
    expect(grant().disabled).toBe(false);
    act(() => {
      grant().form?.requestSubmit();
    });
    await settle();
    expect(granted).toEqual([HOME + "/app"]);
    expect(asked).toEqual(["", "~/ap", "~/app"]);
  });

  it("says why the machine did not answer, and stays open on a refusal", async () => {
    let cancelled = 0;
    draw(
      <RemoteFolderSheet
        machine={MACHINE}
        browse={async () => ({
          entries: [],
          reason: "auth",
          detail: "ssh refused",
        })}
        onSubmit={async () => {
          throw new Error("that path is already a workspace");
        }}
        onCancel={() => cancelled++}
      />,
    );
    await settle();
    expect(status()).toContain("refused the login");
    expect(grant().disabled).toBe(true);

    const form = host.querySelector("form") as HTMLFormElement;
    act(() => {
      form.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
    });
    expect(cancelled).toBe(1);
  });

  it("keeps the Core's refusal under the buttons", async () => {
    const asked: string[] = [];
    draw(
      <RemoteFolderSheet
        machine={MACHINE}
        browse={fakeBrowse(asked)}
        onSubmit={async () => {
          throw new Error("that path is already a workspace");
        }}
        onCancel={() => {}}
      />,
    );
    await settle();
    act(() => {
      grant().form?.requestSubmit();
    });
    await settle();
    expect(host.querySelector(".fy-sheet-error")?.textContent).toContain(
      "already a workspace",
    );
    expect(grant().disabled).toBe(false);
  });
});
