import assert from "node:assert/strict";
import { readFile, stat } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import extension, {
  COMMIT_COMMAND_NAME,
  createCommitCommand,
  parseCommitArgs,
  runCommitWorkflow,
} from "../src/index.ts";
import type { CommandRunner, CommitProposalRequest } from "../src/index.ts";

const fingerprint = "a".repeat(64);
const safeContext = {
  head: "0123456789abcdef",
  context_fingerprint: fingerprint,
  allowed_paths: ["src/example.ts"],
  rejected_paths: [{ path: ".tao/state.json", reason: "local-only path excluded by .tao/" }],
  allowed_diff: "diff --git a/src/example.ts b/src/example.ts\n+safe change\n",
  allowed_diff_truncated: false,
  recent_history: "abc123 feat(example): add prior behavior",
};
const validProposal = JSON.stringify({
  context_fingerprint: fingerprint,
  type: "feat",
  scope: "example",
  summary: "add safe source",
  what: "Add the safe source change selected by Tao.",
  why: "Keep commit ownership inside Tao's validated boundary.",
});

function createdCommit(stdout = "Created local commit abcdef123456 feat(example): add safe source.\n") {
  return { stdout, exitCode: 0 };
}

test("tao-prefixed commit command registers with Pi", () => {
  const registrations: Array<{ name: string; description?: string }> = [];
  extension({
    registerCommand(name, options) {
      registrations.push({ name, description: options.description });
    },
    on() {},
  });

  assert.equal(COMMIT_COMMAND_NAME, "tao-commit");
  assert.deepEqual(registrations.map(({ name }) => name), ["tao-commit"]);
  assert.equal(registrations[0].description, "Prepare a safe local Tao commit");
});

test("commit args parser preserves explicit message, repository, and optional context", () => {
  assert.deepEqual(parseCommitArgs('--message "feat(cli): use tao" --repo-root /work/repo -- Inspect this change'), {
    message: "feat(cli): use tao",
    repoRoot: "/work/repo",
    context: "Inspect this change",
  });
});

test("proposal workflow delegates preflight and finalization to Tao without invoking Git", async () => {
  const repoRoot = path.join(os.tmpdir(), `tao-pi-repo-${process.pid}-delegate`);
  const calls: Array<{ command: string; args: string[] }> = [];
  let proposalPath = "";
  const run: CommandRunner = async (command, args) => {
    calls.push({ command, args });
    assert.equal(command, "tao");
    if (args.includes("--context")) {
      return { stdout: JSON.stringify(safeContext), exitCode: 0 };
    }
    proposalPath = args[args.indexOf("--proposal-file") + 1];
    assert.equal(await readFile(proposalPath, "utf8"), validProposal);
    return createdCommit();
  };

  const result = await runCommitWorkflow(
    { repoRoot, context: "prefer the example scope" },
    {
      run,
      async proposeProposal(request) {
        assert.equal(request.commitContext.context_fingerprint, fingerprint);
        assert.equal(request.context, "prefer the example scope");
        return validProposal;
      },
    },
  );

  assert.equal(result.hash, "abcdef123456");
  assert.deepEqual(calls.map(({ args }) => args[1]), ["--context", "--proposal-file"]);
  assert.equal(path.relative(repoRoot, proposalPath).startsWith(".."), true);
  await assert.rejects(stat(proposalPath), /ENOENT/);
});

test("proposal workflow gives the selected session one repair after Tao rejects content", async () => {
  const requests: CommitProposalRequest[] = [];
  let finalizations = 0;
  const run: CommandRunner = async (command, args) => {
    assert.equal(command, "tao");
    if (args.includes("--context")) {
      return { stdout: JSON.stringify(safeContext), exitCode: 0 };
    }
    finalizations++;
    if (finalizations === 1) {
      return { stdout: "", stderr: "validate standalone commit proposal: unsupported commit type \"wip\"", exitCode: 1 };
    }
    return createdCommit();
  };

  await runCommitWorkflow({ repoRoot: "/work/repo" }, {
    run,
    async proposeProposal(request) {
      requests.push(request);
      return requests.length === 1 ? JSON.stringify({ context_fingerprint: fingerprint, type: "wip" }) : validProposal;
    },
  });

  assert.equal(finalizations, 2);
  assert.equal(requests.length, 2);
  assert.match(requests[1].validationReason ?? "", /unsupported commit type/);
  assert.match(requests[1].invalidProposal ?? "", /"wip"/);
});

test("proposal workflow does not repair stale or non-proposal finalization failures", async () => {
  let proposals = 0;
  const run: CommandRunner = async (_command, args) => args.includes("--context")
    ? { stdout: JSON.stringify(safeContext), exitCode: 0 }
    : { stdout: "", stderr: "standalone commit context is stale", exitCode: 1 };

  await assert.rejects(
    runCommitWorkflow({ repoRoot: "/work/repo" }, {
      run,
      async proposeProposal() {
        proposals++;
        return validProposal;
      },
    }),
    /context is stale/,
  );
  assert.equal(proposals, 1);
});

test("explicit message delegates directly to Tao and preserves canonical body", async () => {
  const message = "feat(cli): delegate commits\n\nWhat:\nSend the complete message to Tao.\n\nWhy:\nKeep validation and Git mutation centralized.";
  const calls: string[][] = [];
  const result = await runCommitWorkflow({ repoRoot: "/work/repo", message }, {
    async run(command, args) {
      assert.equal(command, "tao");
      calls.push(args);
      return createdCommit("Created local commit fedcba654321 feat(cli): delegate commits.\n");
    },
  });

  assert.equal(result.hash, "fedcba654321");
  assert.deepEqual(calls, [["commit", "--message", message, "--repo-root", "/work/repo"]]);
});

test("command handler uses Pi's selected model, safe Tao context, cancellation, and notifications", async () => {
  const prompts: string[] = [];
  const notifications: string[] = [];
  const controller = new AbortController();
  const run: CommandRunner = async (command, args, options) => {
    assert.equal(command, "tao");
    assert.equal(options?.signal, controller.signal);
    return args.includes("--context") ? { stdout: JSON.stringify(safeContext), exitCode: 0 } : createdCommit();
  };
  const command = createCommitCommand({
    run,
    repoRoot: "/work/repo",
    async completeMessage(model, prompt, auth, signal) {
      assert.deepEqual(model, { provider: "test", id: "selected-model" });
      assert.equal(auth.apiKey, "test-key");
      assert.equal(signal, controller.signal);
      prompts.push(prompt);
      return validProposal;
    },
  });

  await command.handler("-- user supplied context", {
    model: { provider: "test", id: "selected-model" },
    modelRegistry: {
      async getApiKeyAndHeaders() {
        return { ok: true, apiKey: "test-key" };
      },
    },
    signal: controller.signal,
    ui: {
      notify(message) {
        notifications.push(message);
      },
    },
  });

  assert.equal(prompts.length, 1);
  assert.match(prompts[0], /Safe context from Tao/);
  assert.match(prompts[0], new RegExp(fingerprint));
  assert.match(prompts[0], /user supplied context/);
  assert.doesNotMatch(prompts[0], /git status|git commit/i);
  assert.deepEqual(notifications, ["Created local commit abcdef123456 feat(example): add safe source."]);
});

test("command handler returns without a model when Tao preflight finds no allowed changes", async () => {
  const notifications: string[] = [];
  let calls = 0;
  const command = createCommitCommand({
    repoRoot: "/work/repo",
    async run(commandName, args) {
      calls++;
      assert.equal(commandName, "tao");
      assert.equal(args.includes("--context"), true);
      return {
        stdout: JSON.stringify({ ...safeContext, allowed_paths: [], allowed_diff: "" }),
        exitCode: 0,
      };
    },
  });

  await command.handler("", {
    ui: {
      notify(message) {
        notifications.push(message);
      },
    },
  });

  assert.equal(calls, 1);
  assert.deepEqual(notifications, ["Nothing to commit: no allowed changes."]);
});

test("command handler reports a missing selected model after Tao preflight", async () => {
  const command = createCommitCommand({
    repoRoot: "/work/repo",
    async run() {
      return { stdout: JSON.stringify(safeContext), exitCode: 0 };
    },
  });

  await assert.rejects(command.handler("", { ui: { notify() {} } }), /no Pi model selected/);
});
