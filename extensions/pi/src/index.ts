import type { ExtensionAPI } from "./pi-api.ts";
import { registerCommitCommand } from "./commit.ts";

// Confirmed against Pi 0.77's local loader: package.json `pi.extensions`
// entries may point at TypeScript modules, and each default export receives an
// API object with registerCommand(name, { description, handler }).
export default function taoPiExtension(pi: ExtensionAPI): void {
  registerCommitCommand(pi);
}

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
export type { ExtensionAPI, ExtensionCommandContext } from "./pi-api.ts";
