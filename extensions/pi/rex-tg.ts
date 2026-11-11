// Written by `rex-tg install`; `rex-tg uninstall` removes it.
// Reports pi session and agent events to `rex-tg hook pi`. Silent by design.
import { spawn } from "node:child_process";

const REX_TG_BIN: string = __REX_TG_BIN__;

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
  pi.on("session_shutdown", (event: any, ctx: any) =>
    report("session_shutdown", ctx, { reason: event?.reason, target_session_file: event?.targetSessionFile }),
  );
  pi.on("agent_start", (_event: any, ctx: any) => report("agent_start", ctx));
  pi.on("agent_end", (_event: any, ctx: any) => report("agent_end", ctx));
}
