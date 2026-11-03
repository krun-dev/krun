import { connect } from "cloudflare:sockets";

async function probeNetwork() {
  let httpBlocked = false;
  let tcpBlocked = false;

  try {
    await fetch("https://example.com", {
      signal: AbortSignal.timeout(2000),
    });
  } catch {
    httpBlocked = true;
  }

  try {
    const socket = connect({ hostname: "example.com", port: 443 });
    await socket.opened;
    await socket.close();
  } catch {
    tcpBlocked = true;
  }

  return { httpBlocked, tcpBlocked };
}

export default {
  async fetch(request, env) {
    const pathname = new URL(request.url).pathname;

    if (pathname === "/api/network") {
      return Response.json(await probeNetwork());
    }
    if (pathname === "/api/assets") {
      return env.ASSETS.fetch(new Request("https://assets.local/hello.txt"));
    }

    return Response.json({
      message: "private-storage-worker",
      bindings: Object.keys(env),
    });
  },
};
