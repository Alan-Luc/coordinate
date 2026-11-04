import { writeSync } from "node:fs";
import { pathToFileURL } from "node:url";

const [codingAgentPath, tuiPath, cwd, sessionDir, allSessionsDir, agentDir] = process.argv.slice(1);
let ui;
let selector;
let settled = false;
const abortControllers = new Set();
const signals = ["SIGINT", "SIGTERM", "SIGHUP"];
const onSignal = () => finish(null);

function finish(path) {
  if (settled) return;
  settled = true;
  for (const controller of abortControllers) controller.abort();
  for (const signal of signals) process.off(signal, onSignal);
  ui?.stop();
  writeSync(3, JSON.stringify({ path }));
}

try {
  const [{ SessionManager, SessionSelectorComponent, initTheme }, { ProcessTerminal, setKeybindings, TuiMainScreen }] = await Promise.all([
    import(pathToFileURL(codingAgentPath)),
    import(pathToFileURL(tuiPath)),
  ]);
  initTheme("dark", false);
  ui = new TuiMainScreen(new ProcessTerminal(), false, agentDir);
  process.stderr.write("Selection-only mode: delete and rename are disabled.\n");
  const load = (all) => async (onProgress, signal) => {
    const controller = new AbortController();
    abortControllers.add(controller);
    signal?.addEventListener("abort", () => controller.abort(), { once: true });
    try {
      return all
        ? await SessionManager.listAll(allSessionsDir || undefined, onProgress, controller.signal)
        : await SessionManager.list(cwd, sessionDir, onProgress, controller.signal);
    } finally {
      abortControllers.delete(controller);
    }
  };
  selector = new SessionSelectorComponent(load(false), load(true), finish, () => finish(null), () => finish(null), () => ui.requestRender(), { showRenameHint: false });
  const sessionList = selector.getSessionList();
  // Pi 1.4.0 keeps its application bindings on the selector, not the TUI registry.
  setKeybindings(sessionList.keybindings);
  sessionList.onDeleteSession = async () => {
    sessionList.onError?.("Deletion is disabled in selection-only mode");
    ui.requestRender();
  };
  sessionList.onRenameSession = undefined;
  ui.addChild(selector);
  ui.setFocus(sessionList);
  for (const signal of signals) process.once(signal, onSignal);
  ui.start();
} catch (error) {
  for (const controller of abortControllers) controller.abort();
  for (const signal of signals) process.off(signal, onSignal);
  ui?.stop();
  process.stderr.write(`native session picker failed: ${error instanceof Error ? error.message : String(error)}\n`);
  writeSync(3, JSON.stringify({ path: null }));
  process.exitCode = 1;
}
