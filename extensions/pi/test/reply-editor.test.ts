import assert from "node:assert/strict";
import { test } from "node:test";

import {
  EXTERNAL_EDITOR_ACTION,
  configureReplyEditor,
  createReplyEditorFactory,
  decideReplyEditorInstallation,
  isExternalEditorInput,
} from "../src/reply-editor.ts";
import type { ExtensionContext, ExtensionUIContext } from "../src/pi-api.ts";

test("external-editor dispatch asks keybindings for Pi's action id first", () => {
  const calls: Array<[string, string]> = [];
  const keybindings = {
    matches(data: string, action: string) {
      calls.push([data, action]);
      return data === "ctrl-g" && action === EXTERNAL_EDITOR_ACTION;
    },
  };

  assert.equal(isExternalEditorInput("ctrl-g", keybindings), true);
  assert.equal(isExternalEditorInput("x", keybindings), false);
  assert.deepEqual(calls, [
    ["ctrl-g", "app.editor.external"],
    ["x", "app.editor.external"],
  ]);
});

test("Ctrl+G excludes project settings when the project is untrusted", async () => {
  const events: string[] = [];
  let compositionFinished!: () => void;
  const finished = new Promise<void>((resolve) => { compositionFinished = resolve; });
  const ctx = makeContext(makeUI());
  ctx.isProjectTrusted = () => false;

  class FakeCustomEditor {
    constructor(_tui: unknown, _theme: unknown, _keybindings: unknown) {}
    render(): string[] { return []; }
    invalidate(): void {}
    getText(): string { return "draft"; }
    setText(text: string): void { events.push(`set:${text}`); }
    handleInput(data: string): void { events.push(`delegate:${data}`); }
  }

  const factory = createReplyEditorFactory(
    ctx,
    FakeCustomEditor,
    {
      async readExternalEditorSetting(cwd, projectTrusted) {
        events.push(`settings:${cwd}:${String(projectTrusted)}`);
        return undefined;
      },
      async compose(_draft, _reference, options) {
        events.push(`compose:${String(options?.settingsEditor)}`);
        compositionFinished();
        return { success: false, exitCode: 1 };
      },
    },
  );
  const editor = factory(
    {
      stop() { events.push("tui:stop"); },
      start() { events.push("tui:start"); },
      requestRender(force) { events.push(`tui:render:${String(force)}`); },
    },
    {},
    { matches: (data: string, action: string) => data === "ctrl-g" && action === EXTERNAL_EDITOR_ACTION },
  ) as FakeCustomEditor;

  editor.handleInput("ctrl-g");
  await finished;
  await new Promise<void>((resolve) => setImmediate(resolve));

  assert.deepEqual(events, [
    "tui:stop",
    "settings:/repo:false",
    "compose:undefined",
    "tui:start",
    "tui:render:true",
  ]);
});

test("installation stands down when another extension owns the editor", async () => {
  const notifications: string[] = [];
  let setCalls = 0;
  const existingFactory = () => ({ render: () => [], invalidate() {} });
  const ctx = makeContext(makeUI({
    getEditorComponent() { return existingFactory; },
    notify(message) { notifications.push(message); },
    setEditorComponent() { setCalls++; },
  }));

  assert.equal(decideReplyEditorInstallation(ctx, {}), "editor-owned");
  const loadRuntime = async () => {
    throw new Error("runtime must not load after stand-down");
  };
  const decision = await configureReplyEditor(ctx, loadRuntime, {});
  const repeatedDecision = await configureReplyEditor(ctx, loadRuntime, {});

  assert.equal(decision, "editor-owned");
  assert.equal(repeatedDecision, "editor-owned");
  assert.equal(setCalls, 0);
  assert.deepEqual(notifications, [
    "Tao reply composer disabled because another extension owns the editor.",
  ]);
});

test("environment opt-out wins without inspecting or replacing the editor", async () => {
  let setCalls = 0;
  const ctx = makeContext(makeUI({
    getEditorComponent() {
      throw new Error("opt-out must not inspect editor ownership");
    },
    setEditorComponent() { setCalls++; },
  }));
  const env = { TAO_PI_REPLY_COMPOSER: "0" };

  assert.equal(decideReplyEditorInstallation(ctx, env), "opt-out");
  const decision = await configureReplyEditor(ctx, async () => {
    throw new Error("runtime must not load after opt-out");
  }, env);

  assert.equal(decision, "opt-out");
  assert.equal(setCalls, 0);
});

function makeContext(ui: ExtensionUIContext): ExtensionContext {
  return {
    ui,
    mode: "tui",
    cwd: "/repo",
    sessionManager: { getBranch: () => [] },
    isProjectTrusted: () => true,
  };
}

function makeUI(overrides: Partial<ExtensionUIContext> = {}): ExtensionUIContext {
  return {
    notify() {},
    getEditorText() { return "draft"; },
    setEditorText() {},
    setEditorComponent() {},
    getEditorComponent() { return undefined; },
    async custom() { throw new Error("not used"); },
    ...overrides,
  } as ExtensionUIContext;
}
