using W = import "/workerd/workerd.capnp";

const config :W.Config = (
  services = [
    (
      name = "entry",
      worker = (
        modules = [
          (name = "entry.js", esModule = embed "/app/entry.js"),
          (name = "assets.js", esModule = embed "/app/assets.js"),
          (name = "service.js", esModule = embed "/app/service.js"),
          (name = "cache.js", esModule = embed "/app/cache.js"),
          (name = "storage.js", esModule = embed "/app/storage.js"),
          (name = "storage/signature.js", esModule = embed "/app/storage/signature.js"),
          (name = "storage/signer.js", esModule = embed "/app/storage/signer.js"),
          (name = "storage/metadata.js", esModule = embed "/app/storage/metadata.js"),
          (name = "storage/xml.js", esModule = embed "/app/storage/xml.js"),
          (name = "storage/binding.js", esModule = embed "/app/storage/binding.js")
        ],
        compatibilityDate = "2026-09-28",
        compatibilityFlags = ["experimental", "enable_ctx_exports", "enable_request_signal", "request_signal_passthrough"],
        bindings = [
          (name = "CACHE_SOURCE", text = embed "/app/cache.js"),
          (name = "R2_BINDING_SOURCE", text = embed "/app/storage/binding.js"),
          (name = "R2_METADATA_SOURCE", text = embed "/app/storage/metadata.js"),
          (name = "R2_XML_SOURCE", text = embed "/app/storage/xml.js"),
__OBJECT_STORAGE_BINDINGS__
__PUBLIC_EGRESS_BINDING__
          (name = "LOADER", workerLoader = (id = __LOADER_ID__)),
          (name = "PLATFORM", service = "platform"),
          (name = "POD_NAME", text = embed "/pod/name"),
          (name = "POD_UID", text = embed "/pod/uid"),
          (name = "RUNTIME_TOKEN", text = embed "/secrets/KRUN_RUNTIME_TOKEN"),
          (name = "MANAGEMENT_TOKEN", text = embed "/secrets/KRUN_MANAGEMENT_TOKEN"),
          (name = "RUNTIME_POOL", text = __POOL__),
          (name = "CONFIG_BASE", text = __CONFIG_BASE__),
__STORAGE_BINDINGS__
          (name = "ASSETS_CACHE_MAX", text = "67108864")
        ],
        globalOutbound = "storage"
      )
    ),
    (
      name = "platform",
      external = __CONTROL_EXTERNAL__
    ),
    (
      name = "storage",
      network = (
        allow = [__STORAGE_ALLOW__],
        tlsOptions = (trustBrowserCas = true)
      )
    ),
__OBJECT_STORAGE_SERVICES__
__PUBLIC_EGRESS_SERVICE__
    (name = "deny", network = (allow = []))
  ],
  sockets = [
    (
      name = "private",
      address = "*:8081",
      http = (),
      service = (name = "entry", entrypoint = "Private")
    ),
    (
      name = "management",
      address = "*:8083",
      http = (),
      service = (name = "entry", entrypoint = "Management")
    ),
    (
      name = "metrics",
      address = "*:8084",
      http = (),
      service = (name = "entry", entrypoint = "Metrics")
    )
  ]
);
