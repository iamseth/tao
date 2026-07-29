import { execFile } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

import type { ExtensionAPI, ExtensionCommandContext } from "./pi-api.ts";

export const COMMIT_COMMAND_NAME = "tao-commit";

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

export interface CommandOptions {
  signal?: AbortSignal;
}

export type CommandRunner = (command: string, args: string[], options?: CommandOptions) => Promise<CommandResult>;
export type CommitProposalProposer = (request: CommitProposalRequest) => Promise<string>;
export type CommitMessageCompleter = (
  model: NonNullable<ExtensionCommandContext["model"]>,
  prompt: string,
  auth: { apiKey: string; headers?: Record<string, string>; env?: Record<string, string> },
  signal?: AbortSignal,
) => Promise<string>;

export interface StandaloneCommitContext {
  head: string;
  context_fingerprint: string;
  allowed_paths: string[];
  rejected_paths: Array<{ path: string; reason: string; staged?: boolean }>;
  allowed_diff: string;
  allowed_diff_truncated: boolean;
  recent_history: string;
  exclusion_sources?: string[];
}

export interface CommitProposalRequest {
  commitContext: StandaloneCommitContext;
  context?: string;
  invalidProposal?: string;
  validationReason?: string;
}

export interface CommitCommandDependencies {
  run?: CommandRunner;
  proposeProposal?: CommitProposalProposer;
  completeMessage?: CommitMessageCompleter;
  repoRoot?: string;
  signal?: AbortSignal;
  tempRoot?: string;
}

export interface CreateCommitResult {
  hash: string;
  summary: string;
  output: string;
}

const execFileAsync = promisify(execFile);

export async function runCommitWorkflow(args: CommitCommandArgs, dependencies: CommitCommandDependencies = {}): Promise<CreateCommitResult> {
  const repoRoot = path.resolve(args.repoRoot ?? dependencies.repoRoot ?? process.cwd());
  const run = dependencies.run ?? createDefaultRunner(repoRoot);
  const signal = dependencies.signal;

  if (args.message) {
    return finalizeCommit(run, ["commit", "--message", args.message, "--repo-root", repoRoot], signal);
  }

  const contextResult = await runTao(run, ["commit", "--context", "--repo-root", repoRoot], signal);
  const commitContext = parseCommitContext(contextResult.stdout);
  if (commitContext.allowed_paths.length === 0) {
    return { hash: "", summary: "", output: "Nothing to commit: no allowed changes." };
  }
  const propose = dependencies.proposeProposal;
  if (!propose) {
    throw new Error("commit proposal unavailable");
  }

  const tempRoot = path.resolve(dependencies.tempRoot ?? os.tmpdir());
  if (isWithin(repoRoot, tempRoot)) {
    throw new Error("refusing to create temporary commit proposal inside the repository");
  }
  const tempDir = await mkdtemp(path.join(tempRoot, "tao-pi-commit-"));
  const proposalPath = path.join(tempDir, "proposal.json");

  try {
    let proposal = (await propose({ commitContext, context: args.context })).trim();
    if (!proposal) {
      throw new Error("commit proposal unavailable");
    }
    await writeFile(proposalPath, proposal, { encoding: "utf8", mode: 0o600 });

    let result = await run("tao", ["commit", "--proposal-file", proposalPath, "--repo-root", repoRoot], { signal });
    if (!commandSucceeded(result) && isInvalidProposalFailure(result)) {
      const reason = commandFailureText(result);
      proposal = (await propose({
        commitContext,
        context: args.context,
        invalidProposal: proposal,
        validationReason: reason,
      })).trim();
      if (!proposal) {
        throw new Error("repaired commit proposal unavailable");
      }
      await writeFile(proposalPath, proposal, { encoding: "utf8", mode: 0o600 });
      result = await run("tao", ["commit", "--proposal-file", proposalPath, "--repo-root", repoRoot], { signal });
    }
    if (!commandSucceeded(result)) {
      throw new Error(`tao commit finalization failed: ${commandFailureText(result)}`);
    }
    return parseCommitResult(result.stdout);
  } finally {
    await rm(tempDir, { recursive: true, force: true }).catch(() => {});
  }
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
      const proposeProposal = dependencies.proposeProposal ?? commitProposalProposerFromContext(ctx, dependencies.completeMessage);
      const result = await runCommitWorkflow(parsed, { ...dependencies, proposeProposal, signal: ctx.signal });
      ctx.ui.notify(result.output, "info");
    },
  };
}

export function registerCommitCommand(pi: ExtensionAPI): CommitCommandContract {
  const command = createCommitCommand();
  pi.registerCommand(command.name, {
    description: command.description,
    handler: command.handler,
  });
  return command;
}

export function createDefaultRunner(cwd: string): CommandRunner {
  return async (command, args, options = {}) => {
    try {
      const { stdout, stderr } = await execFileAsync(command, args, {
        cwd,
        maxBuffer: 20 * 1024 * 1024,
        signal: options.signal,
      });
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

function commitProposalProposerFromContext(
  ctx: ExtensionCommandContext,
  completeMessage: CommitMessageCompleter = completeCommitMessageWithPi,
): CommitProposalProposer {
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

    return completeMessage(ctx.model, buildCommitProposalPrompt(request), { ...auth, apiKey: auth.apiKey }, ctx.signal);
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

function buildCommitProposalPrompt(request: CommitProposalRequest): string {
  const repair = request.invalidProposal
    ? `\nTao rejected the previous proposal below. Repair it once using Tao's exact error.\n\nPrevious proposal:\n${request.invalidProposal}\n\nTao error:\n${request.validationReason}`
    : "";
  return `Propose structured content for one local commit. Use only the Tao-provided safe context and optional user context below. Do not inspect the repository, stage files, run Git, or add Tao-* trailers.\n\nReturn exactly one JSON object and no Markdown with these fields:\n{"context_fingerprint":"<copy exactly from safe context>","type":"<supported conventional type>","scope":"<lowercase narrow scope>","summary":"<lowercase imperative summary, at most 72 characters>","what":"<non-empty description of what changed>","why":"<non-empty reason for the change>"}\n\nSafe context from Tao:\n${JSON.stringify(request.commitContext)}\n\nOptional user context:\n${request.context ?? ""}${repair}`;
}

async function runTao(run: CommandRunner, args: string[], signal?: AbortSignal): Promise<CommandResult> {
  const result = await run("tao", args, { signal });
  if (!commandSucceeded(result)) {
    throw new Error(`tao ${args.slice(0, 2).join(" ")} failed: ${commandFailureText(result)}`);
  }
  return result;
}

async function finalizeCommit(run: CommandRunner, args: string[], signal?: AbortSignal): Promise<CreateCommitResult> {
  const result = await runTao(run, args, signal);
  return parseCommitResult(result.stdout);
}

function parseCommitContext(stdout: string): StandaloneCommitContext {
  let parsed: unknown;
  try {
    parsed = JSON.parse(stdout);
  } catch (error) {
    throw new Error(`tao commit returned invalid context JSON: ${String(error)}`);
  }
  if (typeof parsed !== "object" || parsed === null || !("context_fingerprint" in parsed) || typeof parsed.context_fingerprint !== "string") {
    throw new Error("tao commit returned context without a fingerprint");
  }
  return parsed as StandaloneCommitContext;
}

function parseCommitResult(stdout: string): CreateCommitResult {
  const output = stdout.trim();
  const match = /^Created local commit ([0-9a-f]+) (.+)\.$/.exec(output);
  if (!match) {
    if (output === "Nothing to commit: no allowed changes.") {
      return { hash: "", summary: "", output };
    }
    throw new Error(`unexpected tao commit output: ${output || "<empty>"}`);
  }
  return { hash: match[1], summary: match[2], output };
}

function commandSucceeded(result: CommandResult): boolean {
  return (result.exitCode ?? 0) === 0;
}

function commandFailureText(result: CommandResult): string {
  return (result.stderr || result.stdout || `exit ${result.exitCode ?? 1}`).trim();
}

function isInvalidProposalFailure(result: CommandResult): boolean {
  const failure = commandFailureText(result).toLowerCase();
  return failure.includes("standalone commit proposal") || failure.includes("decode standalone commit proposal");
}

function isWithin(repoRoot: string, candidate: string): boolean {
  const relative = path.relative(repoRoot, candidate);
  return relative === "" || (!relative.startsWith(`..${path.sep}`) && relative !== "..");
}

function tokenizeArgs(args: string): string[] {
  const tokens: string[] = [];
  const pattern = /"([^"\\]*(?:\\.[^"\\]*)*)"|'([^']*)'|(\S+)/g;
  for (const match of args.matchAll(pattern)) {
    tokens.push((match[1] ?? match[2] ?? match[3]).replace(/\\(["\\])/g, "$1"));
  }
  return tokens;
}
