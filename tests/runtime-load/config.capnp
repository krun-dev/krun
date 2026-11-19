using W = import "/workerd/workerd.capnp";

const config :W.Config = (
  services = [(name = "entry", worker = (
    modules = [(name = "entry.js", esModule = embed "entry.js")],
    compatibilityDate = "2026-09-28",
    bindings = [(name = "LOADER", workerLoader = (id = "execution-pool-experiment"))]
  ))],
  sockets = [(name = "http", address = "127.0.0.1:28081", http = (), service = "entry")]
);
