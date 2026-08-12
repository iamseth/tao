export interface ReplyContentBlock {
  type: string;
  text?: string;
}

export interface ReplyBranchEntry {
  type: string;
  message?: {
    role: string;
    content: readonly ReplyContentBlock[];
  };
}

/** Returns the newest settled assistant text available for reply context. */
export function extractReplyContext(entries: readonly ReplyBranchEntry[]): string | undefined {
  for (let index = entries.length - 1; index >= 0; index--) {
    const entry = entries[index];
    if (entry.type !== "message" || entry.message?.role !== "assistant") {
      continue;
    }

    const textBlocks = entry.message.content
      .filter((block) => block.type === "text" && typeof block.text === "string" && block.text.trim().length > 0)
      .map((block) => block.text!.trimEnd());
    if (textBlocks.length > 0) {
      return textBlocks.join("\n\n");
    }
  }

  return undefined;
}
