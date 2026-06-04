# OpenTelemetry to Grafana Cloud

The task definition for CTS-Lite and the collector sidecar starts with revision 6. The task definition is simply named `cts-lite`. See `task_definition.json`.

Traces, metrics, and logs are emitted over OTLP/HTTP to an OpenTelemetry Collector 
running as a **non-essential sidecar** in the same ECS task on AWS.
The collector forwards everything to Grafana Cloud (Tempo / Mimir / Loki).

The collector container picks up the config through an environment variable in the task definition.
Two files in this directory hold the same config in different forms:

- **`config.yaml`** - the human-readable config
- **`config.json`** - the same config collapsed to a single line of JSON
  This is the value you paste (single line) into the `OTELCOL_CONFIG` environment variable in the task definition.

> Keep the two config files in sync.

The collector container must be configured with the docker command `--config=env:OTELCOL_CONFIG`

