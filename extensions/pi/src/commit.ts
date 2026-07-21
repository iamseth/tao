import { execFile } from "node:child_process";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";

import type { ExtensionAPI, ExtensionCommandContext } from "./pi-api.ts";

export const COMMIT_COMMAND_NAME = "commit";
export const LEGACY_COMMIT_COMMAND_NAME = "tao-commit";

export interface CommitCommandArgs {
  message?: string;
  context?: string;
  repoRoot?: string;
}

export interface CommitCommandContract {
  name: typeof COMMIT_COMMAND_NAME;
  description: string;
  parseArgs(args: string): CommitCommandArgs;
  handler(args: string, ctx: ExtensionCommandContext): Promise<void>;
}

export interface CommandResult {
  stdout: string;
  stderr?: string;
  exitCode?: number;
}

export type CommandRunner = (command: string, args: string[]) => Promise<CommandResult>;
export type CommitMessageProposer = (request: CommitMessageRequest) => Promise<string>;
export type CommitMessageCompleter = (
  model: NonNullable<ExtensionCommandContext["model"]>,
  prompt: string,
  auth: { apiKey: string; headers?: Record<string, string>; env?: Record<string, string> },
  signal?: AbortSignal,
) => Promise<string>;

export interface CommitMessageRequest {
  allowedDiff: string;
  recentLog: string;
  context?: string;
  invalidMessage?: string;
  validationReason?: string;
}

export interface CommitCommandDependencies {
  run?: CommandRunner;
  proposeMessage?: CommitMessageProposer;
  completeMessage?: CommitMessageCompleter;
  repoRoot?: string;
}

export interface GitContext {
  statusPorcelain: string;
  stagedDiff: string;
  unstagedDiff: string;
  recentLog: string;
}

export interface RepoExclusions {
  patterns: string[];
  sources: string[];
}

export interface FileChange {
  path: string;
  diff?: string;
}

export interface FileRejection {
  path: string;
  reason: string;
}

export interface AllowedFilesResult {
  allowed: string[];
  rejected: FileRejection[];
}

export interface StageAllowedFilesResult extends AllowedFilesResult {
  unstagedRejected: string[];
}

export interface CommitMessageValidation {
  valid: boolean;
  reason?: string;
  type?: string;
  scope?: string;
  summary?: string;
}

export interface CreateCommitResult {
  hash: string;
  summary: string;
}

const execFileAsync = promisify(execFile);
const PREFERRED_COMMIT_TYPES = new Set(["feat", "fix", "docs", "style", "refactor", "perf", "test", "build", "ci", "chore", "revert"]);
const ALWAYS_EXCLUDED_PATTERNS = [".tao/"];
const GENERATED_PATH_PATTERNS = [
  /^bin\//,
  /^coverage\.out$/,
  /^dist\//,
  /^build\//,
  /^node_modules\//,
  /(^|\/)coverage\//,
  /(^|\/)\.turbo\//,
  /(^|\/)\.next\//,
  /(^|\/)package-lock\.json$/,
  /(^|\/)yarn\.lock$/,
  /(^|\/)pnpm-lock\.yaml$/,
  /\.min\.(js|css)$/,
  /\.generated\./,
];
const FORBIDDEN_PATH_PATTERNS = [
  /(^|\/)\.env(\.|$)/,
  /(^|\/)\.npmrc$/,
  /(^|\/)\.pypirc$/,
  /(^|\/)id_rsa$/,
  /(^|\/)id_ed25519$/,
  /(^|\/)known_hosts$/,
];
const SECRET_PATH_PATTERN = /(^|\/)(secret|secrets|credential|credentials|token|tokens|apikey|api-key|private-key)(\.|-|_|\/|$)/i;
const CREDENTIAL_DIFF_PATTERNS = [
  /aws_access_key_id\s*=/i,
  /aws_secret_access_key\s*=/i,
  /api[_-]?key\s*[:=]\s*['"]?[A-Za-z0-9_\-]{16,}/i,
  /secret[_-]?key\s*[:=]\s*['"]?[A-Za-z0-9_\-]{16,}/i,
  /password\s*[:=]\s*['"][^'"]{8,}['"]/i,
  /-----BEGIN (RSA |OPENSSH |EC |DSA )?PRIVATE KEY-----/i,
  /ghp_[A-Za-z0-9]{20,}/,
];

export async function collectGitContext(run: CommandRunner): Promise<GitContext> {
  const [status, stagedDiff, unstagedDiff, recentLog] = await Promise.all([
    runGit(run, ["status", "--porcelain"]),
    runGit(run, ["diff", "--cached"]),
    runGit(run, ["diff"]),
    runGit(run, ["log", "--oneline", "-12"]),
  ]);

  return {
    statusPorcelain: status.stdout,
    stagedDiff: stagedDiff.stdout,
    unstagedDiff: unstagedDiff.stdout,
    recentLog: recentLog.stdout,
  };
}

export async function readRepoExclusions(repoRoot: string, reader: (filePath: string) => Promise<string> = readFileUtf8): Promise<RepoExclusions> {
  const patterns = new Set(ALWAYS_EXCLUDED_PATTERNS);
  const sources: string[] = [];
  const guidancePath = path.join(repoRoot, "AGENTS.md");

  try {
    const guidance = await reader(guidancePath);
    sources.push("AGENTS.md");
    for (const match of guidance.matchAll(/`([^`]+)`/g)) {
      const token = match[1].trim();
      if (isLocalOnlyToken(token)) {
        patterns.add(token.endsWith("/") ? token : `${token}/`);
      }
    }
    if (/local-only/i.test(guidance)) {
      for (const line of guidance.split(/\r?\n/)) {
        for (const token of line.match(/[A-Za-z0-9._/-]+\/?/g) ?? []) {
          if (isLocalOnlyToken(token)) {
            patterns.add(token.endsWith("/") ? token : `${token}/`);
          }
        }
      }
    }
  } catch (error) {
    if (!isNotFoundError(error)) {
      throw error;
    }
  }

  return { patterns: [...patterns].sort(), sources };
}

export function filterAllowedFiles(changes: FileChange[], exclusions: RepoExclusions = { patterns: ALWAYS_EXCLUDED_PATTERNS, sources: [] }): AllowedFilesResult {
  const allowed: string[] = [];
  const rejected: FileRejection[] = [];
  const exclusionPatterns = new Set([...ALWAYS_EXCLUDED_PATTERNS, ...exclusions.patterns]);

  for (const change of changes) {
    const normalized = normalizeRepoPath(change.path);
    const reason = rejectionReason(normalized, change.diff ?? "", exclusionPatterns);
    if (reason) {
      rejected.push({ path: change.path, reason });
    } else {
      allowed.push(change.path);
    }
  }

  return { allowed, rejected };
}

export async function stageAllowedFiles(run: CommandRunner, exclusions: RepoExclusions = { patterns: ALWAYS_EXCLUDED_PATTERNS, sources: [] }): Promise<StageAllowedFilesResult> {
  const status = await runGit(run, ["status", "--porcelain"]);
  const paths = parsePorcelainPaths(status.stdout);
  const changes = await Promise.all(
    paths.map(async (filePath): Promise<FileChange> => {
      const [staged, unstaged] = await Promise.all([
        runGit(run, ["diff", "--cached", "--", filePath]),
        runGit(run, ["diff", "--", filePath]),
      ]);
      return { path: filePath, diff: `${staged.stdout}\n${unstaged.stdout}` };
    }),
  );
  const filtered = filterAllowedFiles(changes, exclusions);

  for (const rejection of filtered.rejected) {
    await runGit(run, ["restore", "--staged", "--", rejection.path], { allowExitCodes: [0, 1] });
  }
  if (filtered.allowed.length > 0) {
    await runGit(run, ["add", "--", ...filtered.allowed]);
  }

  return { ...filtered, unstagedRejected: filtered.rejected.map((rejection) => rejection.path) };
}

export function validateCommitMessage(message: string): CommitMessageValidation {
  const trimmed = message.trim();
  const match = /^(?<type>[a-z]+)\((?<scope>[a-z0-9-]+)\): (?<summary>[^\s].*)$/.exec(trimmed);
  if (!match?.groups) {
    return { valid: false, reason: "message must match <type>(<scope>): <summary>" };
  }

  const { type, scope, summary } = match.groups;
  if (!PREFERRED_COMMIT_TYPES.has(type)) {
    return { valid: false, reason: `unsupported commit type: ${type}`, type, scope, summary };
  }
  if (summary.length > 72) {
    return { valid: false, reason: "summary must be 72 characters or fewer", type, scope, summary };
  }
  if (/[.!?]$/.test(summary)) {
    return { valid: false, reason: "summary must not end with punctuation", type, scope, summary };
  }

  return { valid: true, type, scope, summary };
}

export async function proposeAndValidateCommitMessage(
  initialMessage: string | undefined,
  request: CommitMessageRequest,
  proposeMessage?: CommitMessageProposer,
): Promise<string> {
  let message = initialMessage?.trim() || (await proposeMessage?.(request))?.trim();
  if (!message) {
    throw new Error("commit message proposal unavailable");
  }

  let validation = validateCommitMessage(message);
  if (validation.valid) {
    return message;
  }

  if (!proposeMessage) {
    throw new Error(`invalid commit message: ${validation.reason}`);
  }

  const repaired = (await proposeMessage({
    ...request,
    invalidMessage: message,
    validationReason: validation.reason,
  })).trim();
  validation = validateCommitMessage(repaired);
  if (!validation.valid) {
    throw new Error(`invalid commit message after repair: ${validation.reason}`);
  }
  return repaired;
}

export async function createCommit(run: CommandRunner, message: string): Promise<CreateCommitResult> {
  const hasStagedChanges = await runGit(run, ["diff", "--cached", "--quiet"], { allowExitCodes: [0, 1] });
  if ((hasStagedChanges.exitCode ?? 0) === 0) {
    throw new Error("refusing to create empty commit: no staged changes");
  }

  await runGit(run, ["commit", "-m", message]);
  const hash = (await runGit(run, ["rev-parse", "HEAD"])).stdout.trim();
  const summary = (await runGit(run, ["show", "--no-patch", "--format=%s", "HEAD"])).stdout.trim();
  return { hash, summary };
}

export async function runCommitWorkflow(args: CommitCommandArgs, dependencies: CommitCommandDependencies = {}): Promise<CreateCommitResult> {
  const run = dependencies.run ?? createDefaultRunner(args.repoRoot ?? dependencies.repoRoot ?? process.cwd());
  const repoRoot = args.repoRoot ?? dependencies.repoRoot ?? process.cwd();
  const exclusions = await readRepoExclusions(repoRoot);
  const staged = await stageAllowedFiles(run, exclusions);
  if (staged.rejected.length > 0) {
    // Re-read context after rejected paths were explicitly removed from the index.
    await runGit(run, ["status", "--porcelain"]);
  }

  const context = await collectGitContext(run);
  const hasStagedChanges = await runGit(run, ["diff", "--cached", "--quiet"], { allowExitCodes: [0, 1] });
  if ((hasStagedChanges.exitCode ?? 0) === 0) {
    throw new Error("nothing to commit: no allowed changes");
  }
  const message = await proposeAndValidateCommitMessage(args.message, {
    allowedDiff: context.stagedDiff,
    recentLog: context.recentLog,
    context: args.context,
  }, dependencies.proposeMessage);

  return createCommit(run, message);
}

export function parseCommitArgs(args: string): CommitCommandArgs {
  const parsed: CommitCommandArgs = {};
  const tokens = tokenizeArgs(args);

  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i];
    if (token === "--message" && tokens[i + 1]) {
      parsed.message = tokens[++i];
      continue;
    }
    if (token === "--repo-root" && tokens[i + 1]) {
      parsed.repoRoot = tokens[++i];
      continue;
    }
    if (token === "--") {
      parsed.context = tokens.slice(i + 1).join(" ");
      break;
    }
  }

  return parsed;
}

export function createCommitCommand(dependencies: CommitCommandDependencies = {}): CommitCommandContract {
  return {
    name: COMMIT_COMMAND_NAME,
    description: "Prepare a safe local Tao commit",
    parseArgs: parseCommitArgs,
    async handler(args, ctx) {
      const parsed = parseCommitArgs(args);
      const proposeMessage = dependencies.proposeMessage ?? commitMessageProposerFromContext(ctx, dependencies.completeMessage);
      const result = await runCommitWorkflow(parsed, { ...dependencies, proposeMessage });
      ctx.ui.notify(`Created local commit ${result.hash.slice(0, 12)} ${result.summary}.`, "info");
    },
  };
}

export function registerCommitCommand(pi: ExtensionAPI): CommitCommandContract {
  const command = createCommitCommand();
  const options = {
    description: command.description,
    handler: command.handler,
  };
  pi.registerCommand(command.name, options);
  pi.registerCommand(LEGACY_COMMIT_COMMAND_NAME, {
    ...options,
    description: `${command.description} (legacy alias)`,
  });
  return command;
}

export function createDefaultRunner(cwd: string): CommandRunner {
  return async (command, args) => {
    try {
      const { stdout, stderr } = await execFileAsync(command, args, { cwd, maxBuffer: 20 * 1024 * 1024 });
      return { stdout: String(stdout), stderr: String(stderr), exitCode: 0 };
    } catch (error) {
      if (typeof error === "object" && error !== null && "stdout" in error && "stderr" in error) {
        const exitCode = "code" in error && typeof error.code === "number" ? error.code : 1;
        return { stdout: String(error.stdout ?? ""), stderr: String(error.stderr ?? ""), exitCode };
      }
      throw error;
    }
  };
}

async function readFileUtf8(filePath: string): Promise<string> {
  return readFile(filePath, "utf8");
}

function commitMessageProposerFromContext(
  ctx: ExtensionCommandContext,
  completeMessage: CommitMessageCompleter = completeCommitMessageWithPi,
): CommitMessageProposer {
  return async (request) => {
    if (!ctx.model) {
      throw new Error("cannot propose a commit message: no Pi model selected");
    }
    if (!ctx.modelRegistry) {
      throw new Error("cannot propose a commit message: Pi model registry unavailable");
    }

    const auth = await ctx.modelRegistry.getApiKeyAndHeaders(ctx.model);
    if (!auth.ok) {
      throw new Error(`cannot authenticate ${ctx.model.provider}/${ctx.model.id}: ${auth.error}`);
    }
    if (!auth.apiKey) {
      throw new Error(`cannot propose a commit message: no API key for ${ctx.model.provider}/${ctx.model.id}`);
    }

    return completeMessage(ctx.model, buildCommitMessagePrompt(request), { ...auth, apiKey: auth.apiKey }, ctx.signal);
  };
}

async function completeCommitMessageWithPi(
  model: NonNullable<ExtensionCommandContext["model"]>,
  prompt: string,
  auth: { apiKey: string; headers?: Record<string, string>; env?: Record<string, string> },
  signal?: AbortSignal,
): Promise<string> {
  // Pi exposes this package to extensions at runtime. Keep the import dynamic so
  // the repository's dependency-free Node tests can load the extension package.
  const moduleName = "@earendil-works/pi-ai/compat";
  const ai = await import(moduleName) as {
    complete: (
      selectedModel: unknown,
      context: { messages: Array<{ role: "user"; content: Array<{ type: "text"; text: string }>; timestamp: number }> },
      options: { apiKey: string; headers?: Record<string, string>; env?: Record<string, string>; signal?: AbortSignal },
    ) => Promise<{ content: Array<{ type: string; text?: string }> }>;
  };
  const response = await ai.complete(
    model,
    {
      messages: [{
        role: "user",
        content: [{ type: "text", text: prompt }],
        timestamp: Date.now(),
      }],
    },
    { ...auth, signal },
  );
  return response.content
    .filter((part) => part.type === "text" && typeof part.text === "string")
    .map((part) => part.text)
    .join("\n")
    .trim();
}

function buildCommitMessagePrompt(request: CommitMessageRequest): string {
  const repair = request.invalidMessage
    ? `\nPrevious invalid message: ${request.invalidMessage}\nValidation failure: ${request.validationReason}\nReturn exactly one repaired message.`
    : "\nReturn exactly one commit message.";
  return `Propose one conventional commit message matching <type>(<scope>): <summary>.\nInfer the narrowest useful scope from the changed code and recent history. Keep the summary imperative, lowercase, and at most 72 characters with no trailing punctuation. Return plain text without Markdown or explanation.\nUse only this allowed staged diff and recent history.\n\nAllowed staged diff:\n${request.allowedDiff}\n\nRecent history:\n${request.recentLog}\n\nUser context:\n${request.context ?? ""}${repair}`;
}

async function runGit(run: CommandRunner, args: string[], options: { allowExitCodes?: number[] } = {}): Promise<CommandResult> {
  const result = await run("git", args);
  const exitCode = result.exitCode ?? 0;
  if (exitCode !== 0 && !options.allowExitCodes?.includes(exitCode)) {
    throw new Error(`git ${args.join(" ")} failed: ${result.stderr || result.stdout || `exit ${exitCode}`}`);
  }
  return result;
}

function parsePorcelainPaths(status: string): string[] {
  const paths = new Set<string>();
  for (const line of status.split(/\r?\n/)) {
    if (!line.trim()) {
      continue;
    }
    const rawPath = line.slice(3);
    const renameParts = rawPath.split(" -> ");
    paths.add(unquotePorcelainPath(renameParts[renameParts.length - 1]));
  }
  return [...paths];
}

function unquotePorcelainPath(filePath: string): string {
  if (!filePath.startsWith('"')) {
    return filePath;
  }
  try {
    return JSON.parse(filePath);
  } catch {
    return filePath.slice(1, -1);
  }
}

function tokenizeArgs(args: string): string[] {
  const tokens: string[] = [];
  const pattern = /"([^"\\]*(?:\\.[^"\\]*)*)"|'([^']*)'|(\S+)/g;
  for (const match of args.matchAll(pattern)) {
    tokens.push((match[1] ?? match[2] ?? match[3]).replace(/\\(["\\])/g, "$1"));
  }
  return tokens;
}

function isNotFoundError(error: unknown): boolean {
  return typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT";
}

function isLocalOnlyToken(token: string): boolean {
  return token === ".tao" || token === ".tao/" || token.startsWith(".tao/") || token === "planning-session.json";
}

function normalizeRepoPath(filePath: string): string {
  return filePath.replace(/\\/g, "/").replace(/^\.\//, "");
}

function rejectionReason(filePath: string, diff: string, exclusions: Set<string>): string | undefined {
  for (const pattern of exclusions) {
    if (matchesExclusion(filePath, pattern)) {
      return `local-only path excluded by ${pattern}`;
    }
  }
  if (GENERATED_PATH_PATTERNS.some((pattern) => pattern.test(filePath))) {
    return "generated artifact";
  }
  if (FORBIDDEN_PATH_PATTERNS.some((pattern) => pattern.test(filePath))) {
    return "forbidden credential path";
  }
  if (SECRET_PATH_PATTERN.test(filePath)) {
    return "secret-looking path";
  }
  if (CREDENTIAL_DIFF_PATTERNS.some((pattern) => pattern.test(addedDiffLines(diff)))) {
    return "credential-looking diff content";
  }
  return undefined;
}

function matchesExclusion(filePath: string, pattern: string): boolean {
  const normalized = normalizeRepoPath(pattern);
  if (normalized.endsWith("/")) {
    return filePath === normalized.slice(0, -1) || filePath.startsWith(normalized);
  }
  return filePath === normalized || filePath.startsWith(`${normalized}/`);
}

function addedDiffLines(diff: string): string {
  return diff
    .split(/\r?\n/)
    .filter((line) => line.startsWith("+") && !line.startsWith("+++"))
    .join("\n");
}
