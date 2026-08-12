import assert from "node:assert/strict";
import { test } from "node:test";

import extension from "../src/index.ts";
import {
  COMPOSE_REPLY_COMMAND_NAME,
  createComposeReplyCommand,
} from "../src/compose-reply.ts";
import type {
  ExtensionContext,
  ExtensionUIContext,
  SessionStartEvent,
  TUI,
} from "../src/pi-api.ts";
import {
  readExternalEditorSettingFiles,
  resolveEditorCommand,
} from "../src/reply-composer.ts";
import type { ReplyComposerResult } from "../src/reply-composer.ts";

test("session start dynamically registers the compose-reply command", async () => {
  const commands: string[] = [];
  let start: ((event: SessionStartEvent, ctx: ExtensionContext) => Promise<void> | void) | undefined;
  extension({
    registerCommand(name) {
      commands.push(name);
    },
    on(event, handler) {
      assert.equal(event, "session_start");
      start = handler;
    },
  });

  assert.deepEqual(commands, ["tao-commit"]);
  const ctx = makeContext(makeUI());
  ctx.mode = "rpc";
  await start?.({ type: "session_start", reason: "startup" }, ctx);
  assert.deepEqual(commands, ["tao-commit", COMPOSE_REPLY_COMMAND_NAME]);
});

test("untrusted projects read only the global external-editor setting", async () => {
  const reads: string[] = [];
  const editor = await readExternalEditorSettingFiles(
    "/agent/settings.json",
    "/repo/.pi/settings.json",
    false,
    async (settingsPath) => {
      reads.push(settingsPath);
      if (settingsPath === "/agent/settings.json") {
        return JSON.stringify({ externalEditor: "global-nvim" });
      }
      return JSON.stringify({ externalEditor: "/repo/untrusted-editor" });
    },
  );

  assert.equal(editor, "global-nvim");
  assert.deepEqual(reads, ["/agent/settings.json"]);
});

test("untrusted projects fall through to environment editor configuration", async () => {
  const editor = await readExternalEditorSettingFiles(
    "/agent/settings.json",
    "/repo/.pi/settings.json",
    false,
    async (settingsPath) => {
      assert.equal(settingsPath, "/agent/settings.json");
      return "{}";
    },
  );

  assert.equal(editor, undefined);
  assert.equal(resolveEditorCommand({
    settingsEditor: editor,
    visual: "env-nvim",
    editor: "env-vim",
    platform: "linux",
  }), "env-nvim");
});

test("successful composition honors project trust and restores the TUI before setting text", async () => {
  const events: string[] = [];
  let mounted = false;
  const ui = makeUI({
    getEditorText() {
      events.push("get-draft");
      return "draft text";
    },
    async custom(factory) {
      events.push("custom:entry");
      let result: ReplyComposerResult | undefined;
      const component = await factory(
        makeTUI(events),
        {},
        {},
        (value) => {
          events.push("done");
          result = value;
        },
      );
      mounted = false;
      assert.equal(typeof component.render, "function");
      events.push("custom:resolved");
      return result!;
    },
    setEditorText(text) {
      events.push(`set:${text}`);
    },
  });
  const ctx = makeContext(ui);
  ctx.isProjectTrusted = () => {
    events.push("project-trust");
    return false;
  };
  ctx.sessionManager = {
    getBranch() {
      events.push("get-branch");
      return [
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "reference" }] } },
      ];
    },
  };

  const command = createComposeReplyCommand(ctx, {
    async loadRuntime() {
      events.push("load-runtime");
      return {
        async readExternalEditorSetting(cwd, projectTrusted) {
          events.push(`settings:${cwd}:${String(projectTrusted)}`);
          return "global-nvim";
        },
      };
    },
    async compose(draft, reference, options) {
      events.push("compose");
      assert.equal(draft, "draft text");
      assert.equal(reference, "reference");
      assert.equal(options?.settingsEditor, "global-nvim");
      return { success: true, text: "edited draft" };
    },
  });

  await command.handler("");

  assert.equal(mounted, false);
  assert.deepEqual(events, [
    "get-draft",
    "get-branch",
    "load-runtime",
    "project-trust",
    "settings:/repo:false",
    "custom:entry",
    "tui:stop",
    "compose",
    "tui:start",
    "tui:render:true",
    "done",
    "custom:resolved",
    "set:edited draft",
  ]);
});

test("failed and cancelled editor outcomes leave the Pi draft untouched", async () => {
  for (const outcome of [
    { success: false, exitCode: 1 },
    { success: false, exitCode: null },
    { success: false, error: new Error("launch failed") },
  ] satisfies ReplyComposerResult[]) {
    let setCalls = 0;
    const ui = makeUI({
      setEditorText() {
        setCalls++;
      },
      async custom(factory) {
        let result: ReplyComposerResult | undefined;
        await factory(makeTUI([]), {}, {}, (value) => { result = value; });
        return result!;
      },
    });
    const command = createComposeReplyCommand(makeContext(ui), {
      async loadRuntime() {
        return { async readExternalEditorSetting() { return undefined; } };
      },
      async compose() {
        return outcome;
      },
    });

    await command.handler("");
    assert.equal(setCalls, 0);
  }
});

test("TUI resume failures cannot prevent custom completion", async () => {
  const renders: boolean[] = [];
  const ui = makeUI({
    async custom(factory) {
      let result: ReplyComposerResult | undefined;
      await factory({
        stop() {},
        start() { throw new Error("resume failed"); },
        requestRender(force) { renders.push(force ?? false); },
      }, {}, {}, (value) => { result = value; });
      return result!;
    },
  });
  const command = createComposeReplyCommand(makeContext(ui), {
    async loadRuntime() {
      return { async readExternalEditorSetting() { return undefined; } };
    },
    async compose() {
      return { success: false, exitCode: 1 };
    },
  });

  await command.handler("");
  assert.deepEqual(renders, [true]);
});

test("non-TUI command calls do not inspect the session or open the composer", async () => {
  let notified = "";
  const ctx = makeContext(makeUI({
    notify(message) {
      notified = message;
    },
  }));
  ctx.mode = "rpc";
  ctx.sessionManager = {
    getBranch() {
      throw new Error("should not inspect branch");
    },
  };
  const command = createComposeReplyCommand(ctx, {
    async loadRuntime() {
      throw new Error("should not load runtime");
    },
  });

  await command.handler("");
  assert.match(notified, /TUI mode/);
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
    getEditorText() { return "original draft"; },
    setEditorText() {},
    setEditorComponent() {},
    getEditorComponent() { return undefined; },
    async custom(factory) {
      let result: unknown;
      await factory(makeTUI([]), {}, {}, (value) => { result = value; });
      return result;
    },
    ...overrides,
  } as ExtensionUIContext;
}

function makeTUI(events: string[]): TUI {
  return {
    stop() { events.push("tui:stop"); },
    start() { events.push("tui:start"); },
    requestRender(force) { events.push(`tui:render:${String(force)}`); },
  };
}
