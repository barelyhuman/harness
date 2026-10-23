import { render } from "preact";
import "./style.css";
import { App } from "./App";
import { initKeys } from "./lib/commands";
import { loadLayout } from "./state/layout";
import { initAgents, refreshProjects } from "./state/agents";
import { initRoles } from "./state/roles";
import { restoreScope } from "./state/scope";
import { registerShellCommands, initShellEvents } from "./state/shellcommands";
import "./state/ui";

initKeys();
registerShellCommands();
initShellEvents();
initAgents();
initRoles();
render(<App />, document.getElementById("app")!);

void (async () => {
  await loadLayout();
  const ps = await refreshProjects();
  await restoreScope(ps[0]?.id);
})();
