using W = import "/workerd/workerd.capnp";

const config :W.Config = (
  services = [(
    name = "entry",
    worker = (
      modules = [(name = "entry.mjs", esModule = embed "/bench/streams/entry.mjs")],
      compatibilityDate = "2026-09-28",
      bindings = [(name = "KRUN_SLOT", text = "krun-unassigned-slot")]
    )
  )],
  sockets = [
    (name = "private", address = "127.0.0.1:8081", http = (), service = (name = "entry", entrypoint = "Private")),
    (name = "management", address = "127.0.0.1:8083", http = (), service = (name = "entry", entrypoint = "Management")),
    (name = "metrics", address = "127.0.0.1:8084", http = (), service = (name = "entry", entrypoint = "Metrics"))
  ]
);
