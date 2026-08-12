import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { access, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import {
  buildEditorArgv,
  buildEditorInvocation,
  composeReply,
  readExternalEditorSettingFiles,
  resolveEditorCommand,
} from "../src/index.ts";
import type { EditorSpawner, ReplyComposerFileSystem } from "../src/index.ts";

test("editor resolution follows settings, VISUAL, EDITOR, and platform defaults", () => {
  assert.equal(resolveEditorCommand({ settingsEditor: "zed --wait", visual: "nvim", editor: "vim", platform: "linux" }), "zed --wait");
  assert.equal(resolveEditorCommand({ settingsEditor: " \t ", visual: "nvim", editor: "vim", platform: "linux" }), "nvim");
  assert.equal(resolveEditorCommand({ visual: "nvim", editor: "vim", platform: "linux" }), "nvim");
  assert.equal(resolveEditorCommand({ editor: "vim", platform: "linux" }), "vim");
  assert.equal(resolveEditorCommand({ platform: "win32" }), "notepad");
  assert.equal(resolveEditorCommand({ platform: "darwin" }), "nano");
});

test("whitespace-only global and trusted-project settings use Pi's fallbacks", async () => {
  const globalEditor = await readExternalEditorSettingFiles(
    "/agent/settings.json",
    "/repo/.pi/settings.json",
    false,
    async () => JSON.stringify({ externalEditor: " \t " }),
  );
  assert.equal(globalEditor, undefined);
  assert.equal(resolveEditorCommand({
    settingsEditor: globalEditor,
    visual: "env-nvim",
    editor: "env-vim",
    platform: "linux",
  }), "env-nvim");

  const projectEditor = await readExternalEditorSettingFiles(
    "/agent/settings.json",
    "/repo/.pi/settings.json",
    true,
    async (settingsPath) => settingsPath === "/agent/settings.json"
      ? JSON.stringify({ externalEditor: "global-nvim" })
      : JSON.stringify({ externalEditor: "\n  " }),
  );
  assert.equal(projectEditor, undefined);
  assert.equal(resolveEditorCommand({
    settingsEditor: projectEditor,
    platform: "darwin",
  }), "nano");
});

test("vim-family argv opens draft and reference in vertical windows", () => {
  const suffix = [
    "-O",
    "/tmp/prompt.md",
    "/tmp/reference.md",
    "-c",
    "wincmd l | setlocal nomodifiable | wincmd h",
  ];
  assert.deepEqual(buildEditorArgv("nvim", "/tmp/prompt.md", "/tmp/reference.md"), ["nvim", ...suffix]);
  assert.deepEqual(buildEditorArgv("/usr/local/bin/nvim", "/tmp/prompt.md", "/tmp/reference.md"), ["/usr/local/bin/nvim", ...suffix]);
  assert.deepEqual(buildEditorArgv("vim", "/tmp/prompt.md", "/tmp/reference.md"), ["vim", ...suffix]);
});

test("other editors and missing references receive only the draft", () => {
  assert.deepEqual(buildEditorInvocation("code --wait", "prompt.md", "reference.md"), {
    command: "code",
    args: ["--wait", "prompt.md"],
  });
  assert.deepEqual(buildEditorArgv("nano", "prompt.md", "reference.md"), ["nano", "prompt.md"]);
  assert.deepEqual(buildEditorArgv("nvim", "prompt.md"), ["nvim", "prompt.md"]);
});

test("clean exit reads only the draft, strips one newline, uses private modes, and cleans up", async () => {
  const observedModes = new Map<string, number>();
  const reads: string[] = [];
  let tempDir = "";
  const fs = instrumentedFileSystem({
    onTempDir(dir) {
      tempDir = dir;
    },
    onWrite(file, mode) {
      observedModes.set(path.basename(file), mode);
    },
    onRead(file) {
      reads.push(file);
    },
  });
  const spawn: EditorSpawner = (_command, args, options) => {
    assert.equal(options.stdio, "inherit");
    assert.equal(options.shell, false);
    const child = new EventEmitter();
    queueMicrotask(async () => {
      await writeFile(args[args.indexOf("-O") + 1], "edited draft\n\n", "utf8");
      child.emit("exit", 0);
    });
    return child as ReturnType<EditorSpawner>;
  };

  const result = await composeReply("original draft", "assistant reference", {
    settingsEditor: "nvim",
    platform: "darwin",
    spawn,
    fs,
  });

  assert.deepEqual(result, { success: true, text: "edited draft\n" });
  assert.deepEqual(reads.map((file) => path.basename(file)), ["prompt.md"]);
  assert.equal(observedModes.get("prompt.md"), 0o600);
  assert.equal(observedModes.get("reference.md"), 0o400);
  await assert.rejects(access(tempDir), /ENOENT/);
});

test("non-zero exit does not read the draft and removes the temp directory", async () => {
  const reads: string[] = [];
  let tempDir = "";
  const fs = instrumentedFileSystem({
    onTempDir(dir) {
      tempDir = dir;
    },
    onRead(file) {
      reads.push(file);
    },
  });

  const result = await composeReply("draft", "reference", {
    settingsEditor: "nano",
    platform: "linux",
    fs,
    spawn: emittingSpawner("exit", 3),
  });

  assert.deepEqual(result, { success: false, exitCode: 3 });
  assert.deepEqual(reads, []);
  await assert.rejects(access(tempDir), /ENOENT/);
});

test("spawn error event does not read the draft and removes the temp directory", async () => {
  const reads: string[] = [];
  let tempDir = "";
  const fs = instrumentedFileSystem({
    onTempDir(dir) {
      tempDir = dir;
    },
    onRead(file) {
      reads.push(file);
    },
  });
  const launchError = new Error("editor unavailable");

  const result = await composeReply("draft", "reference", {
    settingsEditor: "nano",
    platform: "linux",
    fs,
    spawn: emittingSpawner("error", launchError),
  });

  assert.equal(result.success, false);
  assert.equal(result.success ? undefined : result.error, launchError);
  assert.deepEqual(reads, []);
  await assert.rejects(access(tempDir), /ENOENT/);
});

function emittingSpawner(event: "exit", value: number): EditorSpawner;
function emittingSpawner(event: "error", value: Error): EditorSpawner;
function emittingSpawner(event: "exit" | "error", value: number | Error): EditorSpawner {
  return () => {
    const child = new EventEmitter();
    queueMicrotask(() => child.emit(event, value));
    return child as ReturnType<EditorSpawner>;
  };
}

function instrumentedFileSystem(observers: {
  onTempDir?: (dir: string) => void;
  onWrite?: (file: string, mode: number) => void;
  onRead?: (file: string) => void;
}): ReplyComposerFileSystem {
  return {
    async mkdtemp(prefix) {
      const dir = await mkdtemp(prefix);
      observers.onTempDir?.(dir);
      return dir;
    },
    async writeFile(file, data, options) {
      observers.onWrite?.(file, options.mode);
      await writeFile(file, data, options);
      const actualMode = (await stat(file)).mode & 0o777;
      assert.equal(actualMode, options.mode);
    },
    async readFile(file, encoding) {
      observers.onRead?.(file);
      return readFile(file, encoding);
    },
    async rm(file, options) {
      await rm(file, options);
    },
  };
}
