import { spawn } from "node:child_process";
const kubeconfig =
  process.env.KUBECONFIG ||
  "/home/<user>/kind/kubeconfig";
let stopping = false,
  child;
for (const signal of ["SIGINT", "SIGTERM"])
  process.on(signal, () => {
    stopping = true;
    child?.kill(signal);
  });
while (!stopping) {
  child = spawn(
    "kubectl",
    [
      "--kubeconfig",
      kubeconfig,
      "-n",
      "workers-mvp",
      "port-forward",
      "deployment/backend",
      "4380:9000",
      "--address=127.0.0.1",
    ],
    {
      stdio: "inherit",
      env: { ...process.env, KUBECTL_PORT_FORWARD_WEBSOCKETS: "false" },
    },
  );
  await new Promise((resolve) => {
    child.on("exit", resolve);
    child.on("error", resolve);
  });
  if (!stopping) {
    console.error("kind connection closed; reconnecting");
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
}
