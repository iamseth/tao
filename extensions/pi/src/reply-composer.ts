import { spawn as nodeSpawn } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

export interface EditorResolutionOptions {
  settingsEditor?: string;
  visual?: string;
  editor?: string;
  platform?: NodeJS.Platform;
}

export type SettingsFileReader = (settingsPath: string) => Promise<string>;

export interface EditorInvocation {
  command: string;
  args: string[];
}

export interface SpawnedEditor {
  once(event: "error", listener: (error: Error) => void): this;
  once(event: "exit", listener: (code: number | null) => void): this;
}

export type EditorSpawner = (
  command: string,
  args: readonly string[],
  options: { stdio: "inherit"; shell: boolean },
) => SpawnedEditor;

export interface ReplyComposerFileSystem {
  mkdtemp(prefix: string): Promise<string>;
  writeFile(file: string, data: string, options: { encoding: "utf8"; mode: number }): Promise<void>;
  readFile(file: string, encoding: "utf8"): Promise<string>;
  rm(file: string, options: { recursive: true; force: true }): Promise<void>;
}

export interface ReplyComposerOptions extends EditorResolutionOptions {
  spawn?: EditorSpawner;
  fs?: ReplyComposerFileSystem;
  tmpdir?: () => string;
}

export type ReplyComposerResult =
  | { success: true; text: string }
  | { success: false; error?: Error; exitCode?: number | null };

const defaultFileSystem: ReplyComposerFileSystem = { mkdtemp, writeFile, readFile, rm };

/** Resolves Pi's external-editor setting without consulting an untrusted project file. */
export async function readExternalEditorSettingFiles(
  globalSettingsPath: string,
  projectSettingsPath: string,
  projectTrusted: boolean,
  readTextFile: SettingsFileReader = (settingsPath) => readFile(settingsPath, "utf8"),
): Promise<string | undefined> {
  const globalSetting = await readEditorSetting(globalSettingsPath, readTextFile);
  if (!projectTrusted) {
    return globalSetting.editor;
  }
  const projectSetting = await readEditorSetting(projectSettingsPath, readTextFile);
  return projectSetting.defined ? projectSetting.editor : globalSetting.editor;
}

/** Resolves the external editor using the same precedence as Pi. */
export function resolveEditorCommand(options: EditorResolutionOptions = {}): string {
  return nonBlankEditorSetting(options.settingsEditor)
    || options.visual
    || options.editor
    || (options.platform === "win32" ? "notepad" : "nano");
}

/** Builds the command and arguments using Pi's intentionally plain space split. */
export function buildEditorInvocation(
  editorCommand: string,
  draftPath: string,
  referencePath?: string,
): EditorInvocation {
  const [command, ...configuredArgs] = editorCommand.split(" ");
  const args = [...configuredArgs];
  const editorName = path.basename(command).toLowerCase();

  if (referencePath && (editorName === "nvim" || editorName === "vim")) {
    args.push(
      "-O",
      draftPath,
      referencePath,
      "-c",
      "wincmd l | setlocal nomodifiable | wincmd h",
    );
  } else {
    args.push(draftPath);
  }

  return { command, args };
}

/** Returns the complete argv, including the executable as argv[0]. */
export function buildEditorArgv(editorCommand: string, draftPath: string, referencePath?: string): string[] {
  const invocation = buildEditorInvocation(editorCommand, draftPath, referencePath);
  return [invocation.command, ...invocation.args];
}

/** Opens an external editor and returns only the edited draft on a clean exit. */
export async function composeReply(
  draftText: string,
  referenceText?: string,
  options: ReplyComposerOptions = {},
): Promise<ReplyComposerResult> {
  const fileSystem = options.fs ?? defaultFileSystem;
  const platform = options.platform ?? process.platform;
  const tempDir = await fileSystem.mkdtemp(path.join((options.tmpdir ?? os.tmpdir)(), "tao-pi-reply-"));
  const draftPath = path.join(tempDir, "prompt.md");
  const referencePath = referenceText === undefined ? undefined : path.join(tempDir, "reference.md");

  try {
    await fileSystem.writeFile(draftPath, draftText, { encoding: "utf8", mode: 0o600 });
    if (referencePath !== undefined) {
      await fileSystem.writeFile(referencePath, referenceText, { encoding: "utf8", mode: 0o400 });
    }

    const editorCommand = resolveEditorCommand({
      settingsEditor: options.settingsEditor,
      visual: options.visual ?? process.env.VISUAL,
      editor: options.editor ?? process.env.EDITOR,
      platform,
    });
    const invocation = buildEditorInvocation(editorCommand, draftPath, referencePath);
    const outcome = await waitForEditor(
      options.spawn ?? defaultSpawn,
      invocation,
      platform,
    );
    if (!outcome.success) {
      return outcome;
    }

    const text = await fileSystem.readFile(draftPath, "utf8");
    return { success: true, text: text.replace(/\n$/, "") };
  } finally {
    await fileSystem.rm(tempDir, { recursive: true, force: true }).catch(() => {});
  }
}

interface EditorSetting {
  defined: boolean;
  editor?: string;
}

async function readEditorSetting(
  settingsPath: string,
  readTextFile: SettingsFileReader,
): Promise<EditorSetting> {
  try {
    const settings = JSON.parse(await readTextFile(settingsPath)) as { externalEditor?: unknown };
    const defined = Object.prototype.hasOwnProperty.call(settings, "externalEditor");
    return {
      defined,
      editor: defined ? nonBlankEditorSetting(settings.externalEditor) : undefined,
    };
  } catch {
    return { defined: false };
  }
}

function nonBlankEditorSetting(value: unknown): string | undefined {
  return typeof value === "string" && value.trim().length > 0 ? value : undefined;
}

function defaultSpawn(command: string, args: readonly string[], options: { stdio: "inherit"; shell: boolean }): SpawnedEditor {
  return nodeSpawn(command, [...args], options) as SpawnedEditor;
}

function waitForEditor(
  spawnEditor: EditorSpawner,
  invocation: EditorInvocation,
  platform: NodeJS.Platform,
): Promise<ReplyComposerResult> {
  return new Promise((resolve) => {
    let child: SpawnedEditor;
    try {
      child = spawnEditor(invocation.command, invocation.args, {
        stdio: "inherit",
        shell: platform === "win32",
      });
    } catch (error) {
      resolve({ success: false, error: asError(error) });
      return;
    }

    let settled = false;
    child.once("error", (error) => {
      if (!settled) {
        settled = true;
        resolve({ success: false, error });
      }
    });
    child.once("exit", (exitCode) => {
      if (!settled) {
        settled = true;
        resolve(exitCode === 0 ? { success: true, text: "" } : { success: false, exitCode });
      }
    });
  });
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}
