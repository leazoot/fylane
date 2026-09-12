// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { LangContext } from "../lib/i18n";
import type { MachineInfo, ProbeResult } from "../lib/core";
import {
  AddMachineSheet,
  parseTarget,
  suggestName,
  targetLine,
} from "./AddMachineSheet";

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

function draw(node: React.ReactNode) {
  act(() => {
    root.render(
      <LangContext.Provider value={{ lang: "en", setLang: () => {} }}>
        {node}
      </LangContext.Provider>,
    );
  });
}

function type(id: string, value: string) {
  const el = host.querySelector(`#${id}`) as HTMLInputElement;
  act(() => {
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const text = () => host.textContent ?? "";
const submitButton = () =>
  host.querySelector(".fy-sheet-approve") as HTMLButtonElement;
const gateOpen = () =>
  host.querySelector(".fy-gate")?.getAttribute("data-open");

describe("parseTarget", () => {
  it("reads a destination the way it is typed after ssh", () => {
    expect(parseTarget("hk")).toEqual({
      host: "hk",
      user: undefined,
      port: undefined,
    });
    expect(parseTarget("deploy@38.55.193.102:2222")).toEqual({
      host: "38.55.193.102",
      user: "deploy",
      port: 2222,
    });
    expect(parseTarget("ssh -p 2222 deploy@vps.example.com")).toEqual({
      host: "vps.example.com",
      user: "deploy",
      port: 2222,
    });
    expect(parseTarget("  root@box -p2200 ")).toEqual({
      host: "box",
      user: "root",
      port: 2200,
    });
  });
  it("is null until there is a host, and for anything ssh would read as an option", () => {
    for (const bad of [
      "",
      "ssh",
      "-oProxyCommand=x",
      "a b",
      "user@",
      "host:99999",
      "-p 22",
    ]) {
      expect(parseTarget(bad), bad).toBeNull();
    }
  });
  it("suggests a name from the host and shows the destination back", () => {
    expect(suggestName("vps.example.com")).toBe("vps");
    expect(suggestName("hk")).toBe("hk");
    expect(suggestName("38.55.193.102")).toBe("38.55.193.102");
    expect(targetLine({ host: "h", user: "u", port: 22 })).toBe("u@h:22");
  });
});

describe("the add-machine sheet", () => {
  const answer = (over: Partial<ProbeResult> = {}): ProbeResult => ({
    reachable: true,
    running: true,
    compatible: true,
    version: "0.0.4",
    ...over,
  });

  it("knocks once the line settles, opens the gate on an answer, and names the machine after the host", async () => {
    const probed: string[] = [];
    let result = answer();
    draw(
      <AddMachineSheet
        probe={async (m) => {
          probed.push(`${m.user ?? ""}@${m.host}:${m.port ?? ""}`);
          return result;
        }}
        onSubmit={async () => {}}
        onCancel={() => {}}
      />,
    );
    expect(submitButton().disabled).toBe(true);
    expect(text()).toContain("ssh …");
    type("fy-machine-target", "deploy@vps.example.com");
    expect(text()).toContain("ssh deploy@vps.example.com");
    expect(
      (host.querySelector("#fy-machine-name") as HTMLInputElement).value,
    ).toBe("vps");
    expect(text()).toContain("Knocking");
    expect(gateOpen()).toBe("false");
    // Not yet: the line has to settle first.
    expect(probed).toEqual([]);
    await act(async () => {
      vi.advanceTimersByTime(700);
      await Promise.resolve();
    });
    expect(probed).toEqual(["deploy@vps.example.com:"]);
    expect(gateOpen()).toBe("true");
    expect(text()).toContain(
      "deploy@vps.example.com answered · Fylane 0.0.4 is running",
    );
    expect(submitButton().disabled).toBe(false);

    // A machine with nothing on it is an answer too, and says what comes next.
    result = answer({ version: undefined, running: false, compatible: false });
    type("fy-machine-target", "deploy@vps.example.com:2222");
    expect(text()).toContain("ssh -p 2222 deploy@vps.example.com");
    await act(async () => {
      vi.advanceTimersByTime(700);
      await Promise.resolve();
    });
    expect(text()).toContain("no Fylane yet");
  });

  it("says why there was no answer, in the window's words, and still lets the machine in", async () => {
    draw(
      <AddMachineSheet
        probe={async () => ({
          reachable: false,
          running: false,
          compatible: false,
          reason: "auth",
          detail: "ssh refused the login",
        })}
        onSubmit={async () => {}}
        onCancel={() => {}}
      />,
    );
    type("fy-machine-target", "hk");
    await act(async () => {
      vi.advanceTimersByTime(700);
      await Promise.resolve();
    });
    expect(gateOpen()).toBe("false");
    expect(text()).toContain("It refused the login");
    expect(text()).not.toContain("ssh refused the login");
    expect(submitButton().disabled).toBe(false);
  });

  it("a typed name stays put when the host changes", () => {
    draw(
      <AddMachineSheet
        probe={async () => answer()}
        onSubmit={async () => {}}
        onCancel={() => {}}
      />,
    );
    type("fy-machine-target", "hk");
    type("fy-machine-name", "Hong Kong box");
    type("fy-machine-target", "sg");
    expect(
      (host.querySelector("#fy-machine-name") as HTMLInputElement).value,
    ).toBe("Hong Kong box");
  });

  it("editing starts from the machine as it is and saves the correction", async () => {
    const editing: MachineInfo = {
      id: "m_1",
      name: "HK",
      host: "38.55.193.102",
      port: 2222,
      state: "error",
      reason: "auth",
      since: "2026-09-12T08:00:00",
    };
    const saved: unknown[] = [];
    draw(
      <AddMachineSheet
        editing={editing}
        probe={async () => answer()}
        onSubmit={async (m) => {
          saved.push(m);
        }}
        onCancel={() => {}}
      />,
    );
    expect(text()).toContain("Change how HK is reached");
    expect(
      (host.querySelector("#fy-machine-target") as HTMLInputElement).value,
    ).toBe("38.55.193.102:2222");
    expect(
      (host.querySelector("#fy-machine-name") as HTMLInputElement).value,
    ).toBe("HK");
    expect(submitButton().textContent).toBe("Save");
    type("fy-machine-target", "root@38.55.193.102:2222");
    await act(async () => {
      (host.querySelector("form") as HTMLFormElement).dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );
      await Promise.resolve();
    });
    expect(saved).toEqual([
      { name: "HK", host: "38.55.193.102", user: "root", port: 2222 },
    ]);
  });
});
