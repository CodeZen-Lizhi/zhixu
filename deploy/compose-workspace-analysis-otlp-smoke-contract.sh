#!/usr/bin/env bash

set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPOSITORY_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly SMOKE_SCRIPT="${SCRIPT_DIR}/compose-rag-smoke.sh"
readonly WRAPPER_SCRIPT="${SCRIPT_DIR}/compose-workspace-analysis-otlp-smoke.sh"
readonly OVERLAY="${SCRIPT_DIR}/compose.workspace-analysis-otlp-smoke.yml"
readonly COLLECTOR_CONFIG="${SCRIPT_DIR}/workspace-analysis-otel-collector.yaml"

fail() {
  printf '[compose-workspace-analysis-otlp-smoke-contract] %s\n' "$1" >&2
  exit 1
}

for path in "${SMOKE_SCRIPT}" "${WRAPPER_SCRIPT}" "${OVERLAY}" "${COLLECTOR_CONFIG}"; do
  [[ -f "${path}" && ! -L "${path}" ]] || fail "missing regular contract file: ${path##*/}"
done
bash -n "${SMOKE_SCRIPT}" "${WRAPPER_SCRIPT}"

grep -Fq 'ZHIXU_COMPOSE_WORKSPACE_ANALYSIS_OTLP=1' "${WRAPPER_SCRIPT}" || fail 'wrapper does not opt into OTLP Metrics mode'
grep -Fq 'WORKSPACE_ANALYSIS_OTLP_COMPOSE_FILE=' "${SMOKE_SCRIPT}" || fail 'smoke does not load the isolated Collector overlay'
grep -Fq 'assert_workspace_analysis_otlp_metrics' "${SMOKE_SCRIPT}" || fail 'smoke does not verify the Worker metric projection'
grep -Fq "workspace_analysis_outcome_total" "${SMOKE_SCRIPT}" || fail 'smoke does not verify the canonical outcome metric'
grep -Fq '"service_version": "dev"' "${SMOKE_SCRIPT}" || fail 'smoke does not freeze the Worker service-version resource label'
grep -Fq '"job": "zhixu-worker"' "${SMOKE_SCRIPT}" || fail 'smoke does not freeze the Collector job projection'
grep -Fq '"runtime_process_presence"' "${SMOKE_SCRIPT}" || fail 'smoke does not verify the OTLP process-presence metric'
grep -Fq '"runtime_telemetry_required"' "${SMOKE_SCRIPT}" || fail 'smoke does not verify the OTLP required-telemetry metric'
grep -Fq 'labels != expected_labels' "${SMOKE_SCRIPT}" || fail 'smoke does not reject unknown or additional metric labels'
grep -Fq "compose stop --timeout 45 worker" "${SMOKE_SCRIPT}" || fail 'smoke does not exercise graceful Worker metric flush'

grep -Eq 'image: otel/opentelemetry-collector-contrib:0\.158\.0@sha256:[0-9a-f]{64}$' "${OVERLAY}" || \
  fail 'Collector image is not pinned by tag and manifest digest'
grep -Fq 'workspace-analysis-otel-collector:' "${OVERLAY}" || fail 'Collector configuration is not a Compose config'
grep -Fq 'source: workspace-analysis-otel-collector' "${OVERLAY}" || fail 'Collector does not consume the named Compose config'
if grep -Eq '^[[:space:]]+volumes:' "${OVERLAY}"; then
  fail 'Collector configuration must not use a non-runtime host bind mount'
fi
grep -Fq 'network_mode: service:rag-model-fixture' "${OVERLAY}" || fail 'Collector does not share the isolated Worker network namespace'
grep -Fq 'ZHIXU_TELEMETRY_MODE: required' "${OVERLAY}" || fail 'Worker telemetry is not required'
grep -Fq 'OTEL_EXPORTER_OTLP_ENDPOINT: http://127.0.0.1:4318' "${OVERLAY}" || fail 'Worker OTLP endpoint is not loopback-only'

for endpoint in '127.0.0.1:4318' '127.0.0.1:8889' '127.0.0.1:13133'; do
  grep -Fq "endpoint: ${endpoint}" "${COLLECTOR_CONFIG}" || fail "Collector endpoint ${endpoint} is not loopback-bound"
done
grep -Fq 'without_scope_info: true' "${COLLECTOR_CONFIG}" || fail 'Collector Prometheus projection adds unbounded scope labels'
grep -Fq 'resource_to_telemetry_conversion:' "${COLLECTOR_CONFIG}" || fail 'Collector omits the Worker resource projection'
grep -Fq 'exporters: [nop]' "${COLLECTOR_CONFIG}" || fail 'Collector does not safely consume required Trace probes'

grep -Fq 'compose-workspace-analysis-otlp-smoke:' "${REPOSITORY_ROOT}/Makefile" || fail 'Makefile omits the OTLP smoke target'

printf '[compose-workspace-analysis-otlp-smoke-contract] passed\n'
