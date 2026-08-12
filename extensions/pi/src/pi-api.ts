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

export interface ReplyContentBlock {
  type: string;
  text?: string;
}

export interface BranchEntry {
  type: string;
  message?: {
    role: string;
    content: readonly ReplyContentBlock[];
  };
}

export interface SessionManager {
  getBranch(): readonly BranchEntry[];
}

export interface TUI {
  start(): void;
  stop(): void;
  requestRender(force?: boolean): void;
}

export interface TUIComponent {
  render(width: number): string[];
  invalidate(): void;
}

export type EditorFactory = (
  tui: TUI,
  theme: unknown,
  keybindings: unknown,
) => TUIComponent;

export interface ExtensionUIContext extends PiUI {
  custom<T>(
    factory: (
      tui: TUI,
      theme: unknown,
      keybindings: unknown,
      done: (result: T) => void,
    ) => TUIComponent | Promise<TUIComponent>,
  ): Promise<T>;
  getEditorText(): string;
  setEditorText(text: string): void;
  setEditorComponent(factory: EditorFactory | undefined): void;
  getEditorComponent(): EditorFactory | undefined;
}

export interface ExtensionContext {
  ui: ExtensionUIContext;
  mode: "tui" | "rpc" | "json";
  cwd: string;
  sessionManager: SessionManager;
  isProjectTrusted(): boolean;
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

export interface SessionStartEvent {
  type: "session_start";
  reason: string;
  previousSessionFile?: string;
}

export interface ExtensionAPI {
  registerCommand(name: string, options: ExtensionCommandOptions): void;
  on(
    event: "session_start",
    handler: (event: SessionStartEvent, ctx: ExtensionContext) => Promise<void> | void,
  ): void;
}
