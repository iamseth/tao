import type { ExtensionAPI } from "./pi-api.ts";
import { registerCommitCommand } from "./commit.ts";

// Confirmed against Pi 0.77's local loader: package.json `pi.extensions`
// entries may point at TypeScript modules, and each default export receives an
// API object with registerCommand(name, { description, handler }).
export default function taoPiExtension(pi: ExtensionAPI): void {
  registerCommitCommand(pi);
}

export {
  collectGitContext,
  COMMIT_COMMAND_NAME,
  LEGACY_COMMIT_COMMAND_NAME,
  createCommit,
  createCommitCommand,
  createDefaultRunner,
  filterAllowedFiles,
  parseCommitArgs,
  proposeAndValidateCommitMessage,
  readRepoExclusions,
  registerCommitCommand,
  runCommitWorkflow,
  stageAllowedFiles,
  validateCommitMessage,
} from "./commit.ts";
export type {
  AllowedFilesResult,
  CommandResult,
  CommandRunner,
  CommitCommandArgs,
  CommitCommandContract,
  CommitCommandDependencies,
  CommitMessageCompleter,
  CommitMessageRequest,
  CommitMessageValidation,
  CommitMessageProposer,
  CreateCommitResult,
  FileChange,
  FileRejection,
  GitContext,
  RepoExclusions,
  StageAllowedFilesResult,
} from "./commit.ts";
export type { ExtensionAPI, ExtensionCommandContext } from "./pi-api.ts";
