import type { ExtensionAPI } from "./pi-api.ts";
import { registerCommitCommand } from "./commit.ts";

// Confirmed against Pi 0.77's local loader: package.json `pi.extensions`
// entries may point at TypeScript modules, and each default export receives an
// API object with registerCommand(name, { description, handler }).
export default function taoPiExtension(pi: ExtensionAPI): void {
  registerCommitCommand(pi);
  pi.on("session_start", async (_event, ctx) => {
    const { registerComposeReplyCommand } = await import("./compose-reply.ts");
    registerComposeReplyCommand(pi, ctx);

    const { configureReplyEditor } = await import("./reply-editor.ts");
    await configureReplyEditor(ctx, () => import("./pi-runtime.ts"));
  });
}

export { extractReplyContext } from "./reply-context.ts";
export type { ReplyBranchEntry, ReplyContentBlock } from "./reply-context.ts";
export {
  buildEditorArgv,
  buildEditorInvocation,
  composeReply,
  readExternalEditorSettingFiles,
  resolveEditorCommand,
} from "./reply-composer.ts";
export type {
  EditorInvocation,
  EditorResolutionOptions,
  EditorSpawner,
  ReplyComposerFileSystem,
  ReplyComposerOptions,
  ReplyComposerResult,
  SpawnedEditor,
} from "./reply-composer.ts";
export {
  EXTERNAL_EDITOR_ACTION,
  decideReplyEditorInstallation,
  isExternalEditorInput,
} from "./reply-editor.ts";
export type {
  ReplyEditorInstallationDecision,
  ReplyEditorKeybindings,
} from "./reply-editor.ts";
export {
  COMMIT_COMMAND_NAME,
  createCommitCommand,
  createDefaultRunner,
  parseCommitArgs,
  registerCommitCommand,
  runCommitWorkflow,
} from "./commit.ts";
export type {
  CommandOptions,
  CommandResult,
  CommandRunner,
  CommitCommandArgs,
  CommitCommandContract,
  CommitCommandDependencies,
  CommitMessageCompleter,
  CommitProposalProposer,
  CommitProposalRequest,
  CreateCommitResult,
  StandaloneCommitContext,
} from "./commit.ts";
export type {
  BranchEntry,
  EditorFactory,
  ExtensionAPI,
  ExtensionCommandContext,
  ExtensionContext,
  ExtensionUIContext,
  ReplyContentBlock as PiReplyContentBlock,
  TUI,
} from "./pi-api.ts";
