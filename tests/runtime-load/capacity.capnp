using W = import "/workerd/workerd.capnp";

const config :W.Config = (
  services = [(name = "entry", worker = (
    modules = [(name = "entry.js", esModule = embed "capacity-entry.js")],
    compatibilityDate = "2026-09-28",
    bindings = [(name = "LOADER", workerLoader = (id = "execution-pool-capacity"))]
  ))],
  sockets = [(name = "http", address = "127.0.0.1:28081", http = (), service = "entry")]
);
