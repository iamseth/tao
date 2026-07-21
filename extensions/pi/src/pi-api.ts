export interface PiUI {
  notify(message: string, level?: "info" | "warning" | "error"): void;
}

export interface PiModel {
  id: string;
  provider: string;
}

export type PiModelAuthResult =
  | { ok: true; apiKey?: string; headers?: Record<string, string>; env?: Record<string, string> }
  | { ok: false; error: string };

export interface PiModelRegistry {
  getApiKeyAndHeaders(model: PiModel): Promise<PiModelAuthResult>;
}

export interface ExtensionCommandContext {
  ui: PiUI;
  model?: PiModel;
  modelRegistry?: PiModelRegistry;
  signal?: AbortSignal;
}

export interface ExtensionCommandOptions {
  description?: string;
  handler(args: string, ctx: ExtensionCommandContext): Promise<void> | void;
}

export interface ExtensionAPI {
  registerCommand(name: string, options: ExtensionCommandOptions): void;
}
