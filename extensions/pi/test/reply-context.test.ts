import assert from "node:assert/strict";
import { test } from "node:test";

import { extractReplyContext } from "../src/index.ts";
import type { ReplyBranchEntry, ReplyContentBlock } from "../src/index.ts";

const assistantEntry = (content: readonly ReplyContentBlock[]): ReplyBranchEntry => ({
  type: "message",
  message: { role: "assistant", content },
});

test("empty branch has no reply context", () => {
  assert.equal(extractReplyContext([]), undefined);
});

test("branch without an assistant entry has no reply context", () => {
  assert.equal(extractReplyContext([
    { type: "message", message: { role: "user", content: [{ type: "text", text: "question" }] } },
  ]), undefined);
});

test("assistant thinking and tool call blocks do not provide reply context", () => {
  assert.equal(extractReplyContext([
    assistantEntry([
      { type: "thinking" },
      { type: "toolCall" },
    ]),
  ]), undefined);
});

test("empty assistant text blocks do not provide reply context", () => {
  assert.equal(extractReplyContext([
    assistantEntry([
      { type: "text", text: "" },
      { type: "text", text: "  \n\t" },
    ]),
  ]), undefined);
});

test("multiple assistant text blocks are joined with one blank line", () => {
  assert.equal(extractReplyContext([
    assistantEntry([
      { type: "text", text: "first paragraph  \n" },
      { type: "thinking" },
      { type: "text", text: "second paragraph\n\t" },
    ]),
  ]), "first paragraph\n\nsecond paragraph");
});

test("custom messages are ignored", () => {
  assert.equal(extractReplyContext([
    {
      type: "custom_message",
      message: { role: "assistant", content: [{ type: "text", text: "not assistant output" }] },
    },
  ]), undefined);
});

test("older text-bearing assistant entry is used when the newest has no text", () => {
  assert.equal(extractReplyContext([
    assistantEntry([{ type: "text", text: "older answer" }]),
    assistantEntry([{ type: "thinking" }, { type: "toolCall" }]),
  ]), "older answer");
});
