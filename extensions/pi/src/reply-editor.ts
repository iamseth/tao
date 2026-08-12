import type {
  EditorFactory,
  ExtensionContext,
  TUI,
  TUIComponent,
} from "./pi-api.ts";
import { extractReplyContext } from "./reply-context.ts";
import { composeReply } from "./reply-composer.ts";
import type { ReplyComposerOptions, ReplyComposerResult } from "./reply-composer.ts";

export const EXTERNAL_EDITOR_ACTION = "app.editor.external";

export interface ReplyEditorKeybindings {
  matches(data: string, action: string): boolean;
}

export interface CustomEditorInstance extends TUIComponent {
  getExpandedText?(): string;
  getText(): string;
  setText(text: string): void;
  handleInput(data: string): void;
}

export type CustomEditorConstructor = new (
  tui: TUI,
  theme: unknown,
  keybindings: ReplyEditorKeybindings,
) => CustomEditorInstance;

export interface ReplyEditorRuntime {
  CustomEditor: CustomEditorConstructor;
  readExternalEditorSetting(cwd: string, projectTrusted: boolean): Promise<string | undefined>;
}

export interface ReplyEditorDependencies {
  compose?: (
    draftText: string,
    referenceText?: string,
    options?: ReplyComposerOptions,
  ) => Promise<ReplyComposerResult>;
  readExternalEditorSetting(
    cwd: string,
    projectTrusted: boolean,
  ): Promise<string | undefined>;
}

export type ReplyEditorInstallationDecision =
  | "install"
  | "not-tui"
  | "opt-out"
  | "editor-owned";

const editorOwnerNotifications = new WeakSet<ExtensionContext["ui"]>();

export function isExternalEditorInput(
  data: string,
  keybindings: ReplyEditorKeybindings,
): boolean {
  return keybindings.matches(data, EXTERNAL_EDITOR_ACTION);
}

export function decideReplyEditorInstallation(
  ctx: Pick<ExtensionContext, "mode" | "ui">,
  env: Pick<NodeJS.ProcessEnv, "TAO_PI_REPLY_COMPOSER"> = process.env,
): ReplyEditorInstallationDecision {
  if (ctx.mode !== "tui") {
    return "not-tui";
  }
  if (env.TAO_PI_REPLY_COMPOSER === "0") {
    return "opt-out";
  }
  if (ctx.ui.getEditorComponent() !== undefined) {
    return "editor-owned";
  }
  return "install";
}

export async function configureReplyEditor(
  ctx: ExtensionContext,
  loadRuntime: () => Promise<ReplyEditorRuntime>,
  env: Pick<NodeJS.ProcessEnv, "TAO_PI_REPLY_COMPOSER"> = process.env,
): Promise<ReplyEditorInstallationDecision> {
  const decision = decideReplyEditorInstallation(ctx, env);
  if (decision === "editor-owned") {
    if (!editorOwnerNotifications.has(ctx.ui)) {
      editorOwnerNotifications.add(ctx.ui);
      ctx.ui.notify(
        "Tao reply composer disabled because another extension owns the editor.",
        "warning",
      );
    }
    return decision;
  }
  if (decision !== "install") {
    return decision;
  }

  const runtime = await loadRuntime();
  ctx.ui.setEditorComponent(createReplyEditorFactory(ctx, runtime.CustomEditor, {
    readExternalEditorSetting: runtime.readExternalEditorSetting,
  }));
  return decision;
}

export function createReplyEditorFactory(
  ctx: ExtensionContext,
  CustomEditor: CustomEditorConstructor,
  dependencies: ReplyEditorDependencies,
): EditorFactory {
  const runComposer = dependencies.compose ?? composeReply;

  // Pi 0.84.1 internals relied upon: setCustomEditorComponent's actionHandlers
  // copy loop overwrites constructor handlers, the action id is
  // app.editor.external, and handleOpenExternalEditor reads getExpandedText.
  class ReplyEditor extends CustomEditor {
    private readonly replyTui: TUI;
    private readonly replyKeybindings: ReplyEditorKeybindings;

    constructor(tui: TUI, theme: unknown, keybindings: ReplyEditorKeybindings) {
      super(tui, theme, keybindings);
      this.replyTui = tui;
      // CustomEditor's keybindings field is private, so retain the factory value.
      this.replyKeybindings = keybindings;
    }

    handleInput(data: string): void {
      if (isExternalEditorInput(data, this.replyKeybindings)) {
        void this.openReplyComposer();
        return;
      }
      super.handleInput(data);
    }

    private async openReplyComposer(): Promise<void> {
      const draft = this.getExpandedText?.() ?? this.getText();
      // Read through ctx at invocation time because Pi may replace the manager.
      const reference = extractReplyContext(ctx.sessionManager.getBranch());

      try {
        this.replyTui.stop();
        const settingsEditor = await dependencies.readExternalEditorSetting(
          ctx.cwd,
          ctx.isProjectTrusted(),
        );
        const result = await runComposer(draft, reference, { settingsEditor });
        if (result.success) {
          this.setText(result.text);
        }
      } catch {
        // Match Pi's external-editor behavior: preserve the current draft on failure.
      } finally {
        try {
          this.replyTui.start();
        } catch {}
        try {
          this.replyTui.requestRender(true);
        } catch {}
      }
    }
  }

  return (tui, theme, keybindings) => new ReplyEditor(
    tui,
    theme,
    keybindings as ReplyEditorKeybindings,
  );
}
