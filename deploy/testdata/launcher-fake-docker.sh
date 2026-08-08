#!/usr/bin/env bash

set -eu

printf 'ZHIXU_MODEL_SETTINGS_ROLLOUT_ID=%s ZHIXU_MODEL_SETTINGS_PREPARED=%s ZHIXU_APP_RESTART_POLICY=%s ZHIXU_WORKER_RESTART_POLICY=%s %s\n' \
  "${ZHIXU_MODEL_SETTINGS_ROLLOUT_ID:-}" "${ZHIXU_MODEL_SETTINGS_PREPARED:-}" \
  "${ZHIXU_APP_RESTART_POLICY:-}" "${ZHIXU_WORKER_RESTART_POLICY:-}" "$*" >>"${ZHIXU_FAKE_DOCKER_LOG}"

arguments=" $* "
case "${arguments}" in
  *" compose version "*)
    printf 'Docker Compose version v5.1.2\n'
    ;;
  *" buildx version "*)
    printf 'github.com/docker/buildx v0.27.0\n'
    ;;
  *" buildx build "*)
    destination=""
    previous=""
    for argument in "$@"; do
      if [[ "${previous}" == "--output" ]]; then
        destination="${argument#type=local,dest=}"
        break
      fi
      previous="${argument}"
    done
    [[ -n "${destination}" ]] || exit 61
    mkdir -p "${destination}"
    cat >"${destination}/zhixu-workspacectl" <<'EOF'
#!/usr/bin/env bash

set -eu

action="${1:-}"
[[ "${action}" == "switch" || "${action}" == "reconcile" ]] || exit 62
shift
root=""
compose_file=""
env_file=""
grant_override=""
database_url_fd=""
compose_project=""
idempotency_key=""
control_instance_id=""
initialize_git=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --workspace-root) root=$2; shift 2 ;;
    --idempotency-key) idempotency_key=$2; shift 2 ;;
    --compose-file) compose_file=$2; shift 2 ;;
    --env-file) env_file=$2; shift 2 ;;
    --grant-override) grant_override=$2; shift 2 ;;
    --compose-project) compose_project=$2; shift 2 ;;
    --database-url-fd) database_url_fd=$2; shift 2 ;;
    --control-instance-id) control_instance_id=$2; shift 2 ;;
    --initialize-git) initialize_git=1; shift ;;
    *) exit 63 ;;
  esac
done
[[ -n "${compose_file}" && -n "${env_file}" && -n "${grant_override}" && "${compose_project}" == "zhixu" ]] || exit 64
[[ "${control_instance_id}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || exit 64
if [[ "${action}" == "switch" ]]; then
  [[ -n "${root}" && -n "${idempotency_key}" ]] || exit 64
else
  [[ -z "${root}" && -z "${idempotency_key}" && "${initialize_git}" == "0" ]] || exit 64
  if [[ -n "${ZHIXU_FAKE_RECONCILE_ROOT:-}" ]]; then
    root="${ZHIXU_FAKE_RECONCILE_ROOT}"
  elif [[ -f "$(dirname -- "${grant_override}")/workspace-selection" ]]; then
    root="$(python3 - "$(dirname -- "${grant_override}")/workspace-selection" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as source:
    print(json.load(source)["canonical_root"])
PY
)"
  fi
fi
[[ "${database_url_fd}" =~ ^[3-9][0-9]*$ ]] || exit 65
database_url="$(eval "cat <&${database_url_fd}")"
[[ "${database_url}" == postgres://* ]] || exit 66
database_url=""
if [[ "${action}" == "switch" ]]; then
  {
    printf 'switch --workspace-root %s --idempotency-key present' "${root}"
    [[ "${initialize_git}" == "0" ]] || printf ' --initialize-git'
    printf ' --compose-file %s --env-file %s --grant-override %s --compose-project %s --database-url-fd %s --control-instance-id present\n' \
      "${compose_file}" "${env_file}" "${grant_override}" "${compose_project}" "${database_url_fd}"
  } >>"${ZHIXU_FAKE_WORKSPACECTL_LOG}"
else
  printf 'reconcile --compose-file %s --env-file %s --grant-override %s --compose-project %s --database-url-fd %s --control-instance-id present\n' \
    "${compose_file}" "${env_file}" "${grant_override}" "${compose_project}" "${database_url_fd}" \
    >>"${ZHIXU_FAKE_WORKSPACECTL_LOG}"
fi
if [[ "${action}" == "reconcile" && -z "${root}" ]]; then
  printf '{"schema":"workspace-control-result/v1","action":"reconcile","status":"idle","changed":false}\n'
  exit 0
fi
if [[ -n "${ZHIXU_FAKE_WORKSPACECTL_FAIL_ROOT:-}" && "${root}" == "${ZHIXU_FAKE_WORKSPACECTL_FAIL_ROOT}" ]]; then
  exit "${ZHIXU_FAKE_WORKSPACECTL_EXIT:-44}"
fi
python3 - "${root}" "${grant_override}" "${action}" "$(dirname -- "${grant_override}")/workspace-selection" <<'PY'
import hashlib
import json
import os
import sys
import uuid

root, grant_path, action, selection_path = sys.argv[1:]
workspace_id = os.environ.get("ZHIXU_FAKE_WORKSPACECTL_WORKSPACE_ID") or str(
    uuid.uuid5(uuid.NAMESPACE_URL, "zhixu-workspace:" + root)
)
fingerprint = os.environ.get("ZHIXU_FAKE_WORKSPACECTL_FINGERPRINT") or hashlib.sha256(
    ("zhixu-root:" + root).encode("utf-8")
).hexdigest()
service = {
    "environment": {
        "ZHIXU_WORKSPACE_GRANTED_ID": workspace_id,
        "ZHIXU_WORKSPACE_GRANTED_ROOT": root,
        "ZHIXU_WORKSPACE_GRANT_GENERATION": "1",
    },
    "volumes": [{
        "type": "bind",
        "source": root,
        "target": root,
        "bind": {"create_host_path": False},
    }],
}
document = json.dumps({"services": {"app": service, "worker": service}}, separators=(",", ":")) + "\n"
temporary = grant_path + ".fake.tmp"
descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
try:
    view = memoryview(document.encode("utf-8"))
    while view:
        written = os.write(descriptor, view)
        if written <= 0:
            raise OSError("grant write failed")
        view = view[written:]
    os.fsync(descriptor)
finally:
    os.close(descriptor)
os.replace(temporary, grant_path)
changed = action == "switch"
if changed and os.path.isfile(selection_path):
    with open(selection_path, "r", encoding="utf-8") as source:
        changed = json.load(source).get("canonical_root") != root
result = {
    "schema": "workspace-control-result/v1",
    "action": action,
    "status": "reconciled",
    "changed": changed,
    "canonical_root": root,
    "workspace_id": workspace_id,
    "root_fingerprint": fingerprint,
    "binding_version": 1,
}
if action == "switch" and changed:
    result.update({
        "status": "switched",
        "grant_generation": 1,
        "operation_id": str(uuid.uuid5(uuid.NAMESPACE_URL, "zhixu-operation:" + root)),
        "operation_result": "succeeded",
    })
elif action == "switch" or action == "reconcile":
    result["grant_generation"] = 1
if os.environ.get("ZHIXU_FAKE_WORKSPACECTL_INVALID_RESULT") == "1":
    result["unexpected"] = True
print(json.dumps(result, separators=(",", ":")))
PY
EOF
    chmod 0755 "${destination}/zhixu-workspacectl"
    ;;
  *"compose.static-models.yml"*" config --format json "*)
    cat "${ZHIXU_FAKE_STATIC_COMPOSE_MODEL}"
    ;;
  *" config --format json "*)
    cat "${ZHIXU_FAKE_MANAGED_COMPOSE_MODEL}"
    ;;
  *" config --quiet "*)
    ;;
  *" port app 8080 "*)
    [[ "${ZHIXU_FAKE_APP_ENDPOINT:-}" != "unavailable" ]] || exit 1
    printf '%s\n' "${ZHIXU_FAKE_APP_ENDPOINT:-127.0.0.1:${ZHIXU_HTTP_PORT:-8080}}"
    ;;
  *" port postgres 5432 "*)
    printf '%s\n' "${ZHIXU_FAKE_POSTGRES_ENDPOINT:-127.0.0.1:49123}"
    ;;
  *" ps --format json app worker proxy "*)
    if [[ "${ZHIXU_FAKE_RUNTIME_READY:-1}" == "1" ]]; then
      printf '[{"Service":"app","State":"running","Health":"healthy"},{"Service":"worker","State":"running","Health":"healthy"},{"Service":"proxy","State":"running","Health":"healthy"}]\n'
    else
      printf '[{"Service":"app","State":"running","Health":"starting"},{"Service":"worker","State":"running","Health":"starting"},{"Service":"proxy","State":"running","Health":"starting"}]\n'
    fi
    ;;
  *" run --rm --no-deps -T model-settings-key-init "*)
    [[ "${ZHIXU_FAKE_KEY_INIT_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_KEY_INIT_EXIT}"
    ;;
  *" run --rm --no-deps -T migrate "*)
    [[ "${ZHIXU_FAKE_MIGRATE_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_MIGRATE_EXIT}"
    ;;
esac
