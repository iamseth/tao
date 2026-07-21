import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import extension, {
  collectGitContext,
  COMMIT_COMMAND_NAME,
  createCommit,
  createCommitCommand,
  createDefaultRunner,
  filterAllowedFiles,
  parseCommitArgs,
  proposeAndValidateCommitMessage,
  readRepoExclusions,
  runCommitWorkflow,
  stageAllowedFiles,
  validateCommitMessage,
} from "../src/index.ts";
import type { CommandRunner, CommitMessageRequest } from "../src/index.ts";

test("commit command skeleton is importable", () => {
  const command = createCommitCommand();

  assert.equal(COMMIT_COMMAND_NAME, "commit");
  assert.equal(command.name, "commit");
  assert.match(command.description, /safe local Tao commit/);
});

test("commit args parser recognizes optional message and context", () => {
  assert.deepEqual(parseCommitArgs("--message feat: -- Inspect staged files"), {
    message: "feat:",
    context: "Inspect staged files",
  });
});

test("commit command registers with Pi", () => {
  const registrations: Array<{ name: string; description?: string; handler: (args: string, ctx: never) => Promise<void> | void }> = [];

  extension({
    registerCommand(name, options) {
      registrations.push({ name, description: options.description, handler: options.handler });
    },
  });

  const commit = registrations.find((registration) => registration.name === "commit");
  const legacyCommit = registrations.find((registration) => registration.name === "tao-commit");
  assert.ok(commit);
  assert.ok(legacyCommit);
  assert.equal(commit.description, "Prepare a safe local Tao commit");
  assert.match(legacyCommit.description ?? "", /legacy alias/);
});

test("collectGitContext reads status, staged diff, unstaged diff, and recent log", async () => {
  const calls: Array<[string, string[]]> = [];
  const outputs = new Map([
    ["status --porcelain", " M extensions/pi/src/commit.ts\n"],
    ["diff --cached", "staged diff"],
    ["diff", "unstaged diff"],
    ["log --oneline -12", "abc123 feat(pi): add skeleton"],
  ]);
  const runner: CommandRunner = async (command, args) => {
    calls.push([command, args]);
    return { stdout: outputs.get(args.join(" ")) ?? "" };
  };

  assert.deepEqual(await collectGitContext(runner), {
    statusPorcelain: " M extensions/pi/src/commit.ts\n",
    stagedDiff: "staged diff",
    unstagedDiff: "unstaged diff",
    recentLog: "abc123 feat(pi): add skeleton",
  });
  assert.deepEqual(calls, [
    ["git", ["status", "--porcelain"]],
    ["git", ["diff", "--cached"]],
    ["git", ["diff"]],
    ["git", ["log", "--oneline", "-12"]],
  ]);
});

test("collectGitContext handles no changes deterministically", async () => {
  const runner: CommandRunner = async () => ({ stdout: "" });

  assert.deepEqual(await collectGitContext(runner), {
    statusPorcelain: "",
    stagedDiff: "",
    unstagedDiff: "",
    recentLog: "",
  });
});

test("readRepoExclusions always excludes .tao/ and derives local-only guidance", async () => {
  const exclusions = await readRepoExclusions("/repo", async (filePath) => {
    assert.equal(filePath, "/repo/AGENTS.md");
    return "Treat `.tao/` as local-only and never commit it.";
  });

  assert.deepEqual(exclusions.sources, ["AGENTS.md"]);
  assert.ok(exclusions.patterns.includes(".tao/"));
});

test("filterAllowedFiles rejects .tao/ paths and keeps ordinary source", () => {
  const result = filterAllowedFiles([
    { path: ".tao/plans/state.json" },
    { path: "extensions/pi/src/commit.ts" },
  ]);

  assert.deepEqual(result.allowed, ["extensions/pi/src/commit.ts"]);
  assert.deepEqual(result.rejected, [{ path: ".tao/plans/state.json", reason: "local-only path excluded by .tao/" }]);
});

test("filterAllowedFiles rejects generated artifacts and local-only exclusions", () => {
  const result = filterAllowedFiles(
    [
      { path: "bin/tao" },
      { path: "coverage.out" },
      { path: ".tao/cache/state.json" },
      { path: "internal/cli/run.go" },
    ],
    { patterns: [".tao/"], sources: ["AGENTS.md"] },
  );

  assert.deepEqual(result.allowed, ["internal/cli/run.go"]);
  assert.deepEqual(
    result.rejected.map((rejection) => rejection.reason),
    ["generated artifact", "generated artifact", "local-only path excluded by .tao/"],
  );
});

test("filterAllowedFiles rejects obvious secret paths and credential-looking diff content", () => {
  const result = filterAllowedFiles([
    { path: ".env" },
    { path: "config/secrets.yml" },
    { path: "src/config.ts", diff: "+const apiKey = '1234567890abcdef1234567890';\n" },
  ]);

  assert.deepEqual(result.allowed, []);
  assert.deepEqual(
    result.rejected.map((rejection) => rejection.reason),
    ["forbidden credential path", "secret-looking path", "credential-looking diff content"],
  );
});

test("validateCommitMessage accepts preferred conventional messages", () => {
  assert.deepEqual(validateCommitMessage("feat(pi): add git safety units"), {
    valid: true,
    type: "feat",
    scope: "pi",
    summary: "add git safety units",
  });
});

test("validateCommitMessage rejects invalid conventional commit messages", () => {
  assert.equal(validateCommitMessage("feat: add git safety units").valid, false);
  assert.equal(validateCommitMessage("wip(pi): add git safety units").reason, "unsupported commit type: wip");
  assert.equal(validateCommitMessage("feat(telemetry): add git safety units").valid, true);
  assert.equal(validateCommitMessage("feat(pi): add git safety units.").reason, "summary must not end with punctuation");
});

test("stageAllowedFiles unstages existing forbidden paths and stages allowed files", async () => {
  const repo = await createGitRepo();
  const run = createDefaultRunner(repo);
  await writeFile(path.join(repo, "src.ts"), "allowed\n");
  await mkdir(path.join(repo, ".tao"), { recursive: true });
  await writeFile(path.join(repo, ".tao", "state.json"), "{}\n");
  await run("git", ["add", "src.ts"]);
  await run("git", ["add", "-f", ".tao/state.json"]);

  const result = await stageAllowedFiles(run);

  assert.deepEqual(result.allowed, ["src.ts"]);
  assert.deepEqual(result.rejected, [{ path: ".tao/state.json", reason: "local-only path excluded by .tao/" }]);
  assert.match((await run("git", ["diff", "--cached", "--name-only"])).stdout, /^src\.ts\n$/);
});

test("runCommitWorkflow leaves excluded files unstaged and commits only allowed files", async () => {
  const repo = await createGitRepo();
  await writeFile(path.join(repo, "AGENTS.md"), "Treat `.tao/` as local-only.\n");
  await writeFile(path.join(repo, "src.ts"), "allowed\n");
  await mkdir(path.join(repo, ".tao"), { recursive: true });
  await writeFile(path.join(repo, ".tao", "state.json"), "{}\n");

  const run = createDefaultRunner(repo);
  await run("git", ["add", "-f", ".tao/state.json"]);
  const result = await runCommitWorkflow({ message: "feat(pi): add commit command", repoRoot: repo }, { run, repoRoot: repo });

  assert.match(result.hash, /^[a-f0-9]{40}$/);
  assert.equal(result.summary, "feat(pi): add commit command");
  assert.equal((await run("git", ["show", "--name-only", "--format=", "HEAD"])).stdout.trim(), "AGENTS.md\nsrc.ts");
});

test("createCommit refuses empty commits", async () => {
  const repo = await createGitRepo();
  await assert.rejects(
    createCommit(createDefaultRunner(repo), "feat(pi): add commit command"),
    /refusing to create empty commit/,
  );
});

test("proposeAndValidateCommitMessage allows one repair for invalid messages", async () => {
  const requests: CommitMessageRequest[] = [];
  const message = await proposeAndValidateCommitMessage(undefined, { allowedDiff: "diff", recentLog: "log" }, async (request) => {
    requests.push(request);
    return requests.length === 1 ? "wip(pi): bad" : "feat(pi): add commit command";
  });

  assert.equal(message, "feat(pi): add commit command");
  assert.equal(requests.length, 2);
  assert.equal(requests[1].validationReason, "unsupported commit type: wip");
});

test("proposeAndValidateCommitMessage fails clearly after one invalid repair", async () => {
  await assert.rejects(
    proposeAndValidateCommitMessage(undefined, { allowedDiff: "diff", recentLog: "log" }, async () => "wip(pi): bad"),
    /invalid commit message after repair: unsupported commit type: wip/,
  );
});

test("command handler proposes a message with the selected Pi model", async () => {
  const repo = await createGitRepo();
  await writeFile(path.join(repo, "src.ts"), "allowed\n");
  const prompts: string[] = [];
  const command = createCommitCommand({
    run: createDefaultRunner(repo),
    repoRoot: repo,
    async completeMessage(model, prompt, auth) {
      assert.deepEqual(model, { provider: "test", id: "commit-model" });
      assert.equal(auth.apiKey, "test-key");
      prompts.push(prompt);
      return "feat(example): add allowed source";
    },
  });

  await command.handler("", {
    model: { provider: "test", id: "commit-model" },
    modelRegistry: {
      async getApiKeyAndHeaders() {
        return { ok: true, apiKey: "test-key" };
      },
    },
    ui: { notify() {} },
  });

  assert.equal(prompts.length, 1);
  assert.match(prompts[0], /Allowed staged diff:/);
  assert.equal((await createDefaultRunner(repo)("git", ["show", "--no-patch", "--format=%s", "HEAD"])).stdout.trim(), "feat(example): add allowed source");
});

test("command handler reports a missing selected model clearly", async () => {
  const repo = await createGitRepo();
  await writeFile(path.join(repo, "src.ts"), "allowed\n");
  const command = createCommitCommand({ run: createDefaultRunner(repo), repoRoot: repo });

  await assert.rejects(command.handler("", { ui: { notify() {} } }), /no Pi model selected/);
});

test("command handler creates a local commit and reports hash", async () => {
  const repo = await createGitRepo();
  await writeFile(path.join(repo, "src.ts"), "allowed\n");
  const notifications: string[] = [];
  const command = createCommitCommand({ run: createDefaultRunner(repo), repoRoot: repo });

  await command.handler('--message "feat(pi): add commit command"', {
    ui: {
      notify(message: string) {
        notifications.push(message);
      },
    },
  });

  assert.match(notifications[0], /^Created local commit [a-f0-9]{12} feat\(pi\): add commit command\.$/);
});

async function createGitRepo(): Promise<string> {
  const repo = await mkdir(path.join(os.tmpdir(), `tao-pi-commit-${process.pid}-${Math.random().toString(16).slice(2)}`), { recursive: true });
  const run = createDefaultRunner(repo);
  await run("git", ["init"]);
  await run("git", ["config", "user.email", "tao@example.test"]);
  await run("git", ["config", "user.name", "Tao Test"]);
  await writeFile(path.join(repo, "README.md"), "initial\n");
  await run("git", ["add", "README.md"]);
  await run("git", ["commit", "-m", "chore(test): initial commit"]);
  return repo;
}
