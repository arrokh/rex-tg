// Written by `rex-tg install`; `rex-tg uninstall` removes it.
// Reports pi session and agent events to `rex-tg hook pi`. Silent by design.
// While pi writes an answer, its text goes to a small file in REX_TG_LIVE, so
// that rex-tg can show it in Telegram as it grows (display.stream).
import { spawn } from "node:child_process";
import { mkdirSync, renameSync, unlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const REX_TG_BIN: string = __REX_TG_BIN__;
const REX_TG_LIVE: string = __REX_TG_LIVE__;

// The live file is written at most this often.
const LIVE_EVERY_MS = 300;
let liveTimer: ReturnType<typeof setTimeout> | undefined;
let livePending: { file: string; body: string } | undefined;
let liveLast = 0;

function liveFile(ctx: any): string | undefined {
  // Only panes started by Rex carry REX_BLOCK.
  if (!process.env.REX_BLOCK || !REX_TG_LIVE) return undefined;
  const id = String(ctx?.sessionManager?.getSessionId?.() ?? "").replace(/[^A-Za-z0-9_-]/g, "");
  return id ? join(REX_TG_LIVE, id + ".json") : undefined;
}

function flushLive(): void {
  liveTimer = undefined;
  const p = livePending;
  livePending = undefined;
  if (!p) return;
  liveLast = Date.now();
  try {
    mkdirSync(REX_TG_LIVE, { recursive: true, mode: 0o700 });
    const tmp = p.file + ".tmp";
    writeFileSync(tmp, p.body, { mode: 0o600 });
    renameSync(tmp, p.file);
  } catch {
    // Never disturb pi.
  }
}

// writeLive keeps the text of the assistant message that pi is writing.
function writeLive(ctx: any, message: any): void {
  try {
    if (message?.role !== "assistant") return;
    const file = liveFile(ctx);
    if (!file) return;
    const parts = Array.isArray(message?.content) ? message.content : [];
    const text = parts
      .filter((c: any) => c?.type === "text" && typeof c.text === "string")
      .map((c: any) => c.text)
      .join("\n\n");
    if (!text.trim()) return;
    livePending = { file, body: JSON.stringify({ session_id: ctx?.sessionManager?.getSessionId?.(), text }) };
    if (liveTimer) return;
    liveTimer = setTimeout(flushLive, Math.max(0, LIVE_EVERY_MS - (Date.now() - liveLast)));
    (liveTimer as any)?.unref?.();
  } catch {
    // Never disturb pi.
  }
}

// clearLive removes the live file: the message is complete, so its text is
// in the session file.
function clearLive(ctx: any): void {
  try {
    if (liveTimer) clearTimeout(liveTimer);
    liveTimer = undefined;
    livePending = undefined;
    const file = liveFile(ctx);
    if (file) unlinkSync(file);
  } catch {
    // Never disturb pi.
  }
}

type Extra = { reason?: string; previous_session_file?: string; target_session_file?: string };

function report(name: string, ctx: any, extra: Extra = {}): void {
  try {
    // Only panes started by Rex carry REX_BLOCK.
    if (!process.env.REX_BLOCK) return;
    const sm = ctx?.sessionManager;
    const payload = {
      hook_event_name: name,
      reason: extra.reason,
      session_file: sm?.getSessionFile?.(),
      previous_session_file: extra.previous_session_file,
      target_session_file: extra.target_session_file,
      cwd: process.cwd(),
      session_id: sm?.getSessionId?.(),
    };
    const child = spawn(REX_TG_BIN, ["hook", "pi"], {
      stdio: ["pipe", "ignore", "ignore"],
      timeout: 3000,
      detached: false,
    });
    child.on("error", () => {});
    child.stdin?.on("error", () => {});
    child.stdin?.end(JSON.stringify(payload));
    child.unref();
  } catch {
    // Never disturb pi.
  }
}

export default function (pi: any) {
  pi.on("session_start", (event: any, ctx: any) =>
    report("session_start", ctx, { reason: event?.reason, previous_session_file: event?.previousSessionFile }),
  );
  pi.on("session_shutdown", (event: any, ctx: any) => {
    clearLive(ctx);
    report("session_shutdown", ctx, { reason: event?.reason, target_session_file: event?.targetSessionFile });
  });
  pi.on("agent_start", (_event: any, ctx: any) => report("agent_start", ctx));
  pi.on("agent_end", (_event: any, ctx: any) => {
    clearLive(ctx);
    report("agent_end", ctx);
  });
  pi.on("message_update", (event: any, ctx: any) => writeLive(ctx, event?.message));
  pi.on("message_end", (_event: any, ctx: any) => clearLive(ctx));
}
