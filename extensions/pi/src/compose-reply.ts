import type {
  ExtensionAPI,
  ExtensionContext,
  ExtensionCommandOptions,
  TUI,
} from "./pi-api.ts";
import { extractReplyContext } from "./reply-context.ts";
import { composeReply } from "./reply-composer.ts";
import type { ReplyComposerOptions, ReplyComposerResult } from "./reply-composer.ts";

export const COMPOSE_REPLY_COMMAND_NAME = "tao-compose-reply";

export interface ComposeReplyRuntime {
  readExternalEditorSetting(cwd: string, projectTrusted: boolean): Promise<string | undefined>;
}

export interface ComposeReplyDependencies {
  compose?: (
    draftText: string,
    referenceText?: string,
    options?: ReplyComposerOptions,
  ) => Promise<ReplyComposerResult>;
  loadRuntime?: () => Promise<ComposeReplyRuntime>;
}

export interface ComposeReplyCommand {
  name: string;
  description: string;
  handler(args: string): Promise<void>;
}

export function createComposeReplyCommand(
  ctx: ExtensionContext,
  dependencies: ComposeReplyDependencies = {},
): ComposeReplyCommand {
  const runComposer = dependencies.compose ?? composeReply;
  const loadRuntime = dependencies.loadRuntime ?? (() => import("./pi-runtime.ts"));

  return {
    name: COMPOSE_REPLY_COMMAND_NAME,
    description: "Compose a reply with the latest assistant message as reference",
    async handler() {
      if (ctx.mode !== "tui") {
        ctx.ui.notify("Reply composition is only available in TUI mode.", "warning");
        return;
      }

      const draft = ctx.ui.getEditorText();
      // Read through ctx at invocation time: Pi may replace the session manager.
      const reference = extractReplyContext(ctx.sessionManager.getBranch());
      const runtime = await loadRuntime();
      const settingsEditor = await runtime.readExternalEditorSetting(
        ctx.cwd,
        ctx.isProjectTrusted(),
      );

      const result = await ctx.ui.custom<ReplyComposerResult>(async (tui, _theme, _keybindings, done) => {
        const composerResult = await runSuspended(
          tui,
          () => runComposer(draft, reference, { settingsEditor }),
        );
        done(composerResult);
        return placeholderComponent;
      });

      if (result.success) {
        // showExtensionCustom snapshots editor.getText() on entry and restoreEditor()
        // re-applies it on close, so update the editor only after custom() resolves.
        ctx.ui.setEditorText(result.text);
      }
    },
  };
}

export function registerComposeReplyCommand(
  pi: ExtensionAPI,
  ctx: ExtensionContext,
  dependencies: ComposeReplyDependencies = {},
): ComposeReplyCommand {
  const command = createComposeReplyCommand(ctx, dependencies);
  pi.registerCommand(command.name, {
    description: command.description,
    handler: command.handler as ExtensionCommandOptions["handler"],
  });
  return command;
}

const placeholderComponent = {
  render(): string[] {
    return [];
  },
  invalidate(): void {},
};

async function runSuspended(
  tui: TUI,
  run: () => Promise<ReplyComposerResult>,
): Promise<ReplyComposerResult> {
  try {
    tui.stop();
    return await run();
  } catch (error) {
    return { success: false, error: asError(error) };
  } finally {
    try {
      tui.start();
    } catch {}
    try {
      tui.requestRender(true);
    } catch {}
  }
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}
