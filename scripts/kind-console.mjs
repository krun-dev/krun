import { spawn, execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
const root = fileURLToPath(new URL("../", import.meta.url));
const remote = process.env.KRUN_KIND_SSH || "<user>@<kind-host>";
const setup = `import pathlib,subprocess,os
root=pathlib.Path('/home/<user>/krun')
out=root/'deploy/workers-kind/generated';out.mkdir(exist_ok=True)
pidfile=out/'console-forward.pid'
running=False
if pidfile.exists():
 try:
  pid=int(pidfile.read_text());running='kind-forward.mjs' in pathlib.Path('/proc/'+str(pid)+'/cmdline').read_text()
 except (ValueError,OSError):pass
if not running:
 log=open(out/'console-forward.log','ab')
 child=subprocess.Popen(['node',str(root/'scripts/kind-forward.mjs')],cwd=root,stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True)
 pidfile.write_text(str(child.pid))
print('kind backend forwarding is running on remote loopback')
`;
execFileSync("ssh", ["-o", "ConnectTimeout=10", remote, "python3 -"], {
  input: setup,
  stdio: ["pipe", "inherit", "inherit"],
});
let stopping = false;
const children = new Set();
function run(command, args, env) {
  const child = spawn(command, args, { cwd: root, env, stdio: "inherit" });
  children.add(child);
  child.on("error", (e) => console.error(e.message));
  child.on("close", () => {
    children.delete(child);
    if (!stopping)
      setTimeout(() => {
        if (!stopping) run(command, args, env);
      }, 1500);
  });
}
run(
  "ssh",
  [
    "-N",
    "-o",
    "ExitOnForwardFailure=yes",
    "-o",
    "ServerAliveInterval=15",
    "-o",
    "ServerAliveCountMax=3",
    "-L",
    "127.0.0.1:4381:127.0.0.1:4380",
    remote,
  ],
  process.env,
);
run(
  process.execPath,
  [
    "node_modules/vite/bin/vite.js",
    "--host",
    "127.0.0.1",
    "--port",
    "4380",
    "--strictPort",
  ],
  { ...process.env, KRUN_API_TARGET: "http://127.0.0.1:4381" },
);
for (const signal of ["SIGINT", "SIGTERM"])
  process.on(signal, () => {
    stopping = true;
    for (const child of children) child.kill(signal);
  });
