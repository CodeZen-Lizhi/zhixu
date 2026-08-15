#!/usr/bin/env bash

set -eu

readonly FAKE_STATE_DIR="${ZHIXU_FAKE_STATE_DIR:?}"
readonly NETNS_APP_STATE="${FAKE_STATE_DIR}/netns-app"
readonly NETNS_WORKER_STATE="${FAKE_STATE_DIR}/netns-worker"
readonly NETNS_NETWORK_STATE="${FAKE_STATE_DIR}/netns-network"
readonly NETNS_PORT_STATE="${FAKE_STATE_DIR}/netns-port"
readonly NETNS_HTTP_PID_STATE="${FAKE_STATE_DIR}/netns-http-pid"
readonly NETNS_HTTP_CHILD_PID_STATE="${FAKE_STATE_DIR}/netns-http-child-pid"
readonly NETNS_HTTP_READY_STATE="${FAKE_STATE_DIR}/netns-http-ready"
readonly NETNS_HTTP_MODE_STATE="${FAKE_STATE_DIR}/netns-http-mode"
readonly NETNS_SECURITY_DRIFT_STATE="${FAKE_STATE_DIR}/netns-security-drift"
readonly NETNS_APP_IMAGE_STATE="${FAKE_STATE_DIR}/netns-app-image"
readonly NETNS_WORKER_IMAGE_STATE="${FAKE_STATE_DIR}/netns-worker-image"
readonly DESIRED_APP_IMAGE_STATE="${FAKE_STATE_DIR}/desired-app-image"
readonly DESIRED_WORKER_IMAGE_STATE="${FAKE_STATE_DIR}/desired-worker-image"
readonly LEGACY_MODEL_CONTAINER_STATE="${FAKE_STATE_DIR}/legacy-model-container"
readonly LEGACY_MODEL_RUNNING_STATE="${FAKE_STATE_DIR}/legacy-model-running"
readonly LEGACY_MODEL_VOLUME_STATE="${FAKE_STATE_DIR}/legacy-model-volume"
readonly MANAGED_MODEL_VOLUME_STATE="${FAKE_STATE_DIR}/managed-model-volume"
readonly MANAGED_MODEL_MIGRATION_STATE="${FAKE_STATE_DIR}/managed-model-migration"
readonly LEGACY_MODEL_IMAGE_ID="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
readonly MANAGED_MODEL_IMAGE_ID="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

mkdir -p "${FAKE_STATE_DIR}"

netns_exists() {
  [[ -f "${NETNS_APP_STATE}" && -f "${NETNS_WORKER_STATE}" && -f "${NETNS_NETWORK_STATE}" ]]
}

netns_port() {
  if [[ -f "${NETNS_PORT_STATE}" ]]; then
    cat "${NETNS_PORT_STATE}"
  else
    printf '%s\n' "${ZHIXU_HTTP_PORT:-8080}"
  fi
}

create_netns() {
  local port pid attempt
  if netns_exists && [[ -f "${NETNS_HTTP_PID_STATE}" ]] \
    && kill -0 "$(cat "${NETNS_HTTP_PID_STATE}")" 2>/dev/null; then
    return
  fi
  remove_netns
  port="${ZHIXU_HTTP_PORT:-8080}"
  : >"${NETNS_APP_STATE}"
  : >"${NETNS_WORKER_STATE}"
  : >"${NETNS_NETWORK_STATE}"
  printf '%s\n' "${port}" >"${NETNS_PORT_STATE}"
  cp "${DESIRED_APP_IMAGE_STATE}" "${NETNS_APP_IMAGE_STATE}"
  cp "${DESIRED_WORKER_IMAGE_STATE}" "${NETNS_WORKER_IMAGE_STATE}"
  rm -f "${NETNS_HTTP_READY_STATE}"
  python3 - "${port}" "${NETNS_HTTP_READY_STATE}" "${NETNS_HTTP_MODE_STATE}" "${NETNS_HTTP_CHILD_PID_STATE}" >/dev/null 2>&1 <<'PY' &
import http.server
import os
import signal
import sys
import time

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/readyz":
            self.send_error(404)
            return
        try:
            with open(sys.argv[3], "r", encoding="ascii") as source:
                mode = source.read().strip()
        except OSError:
            mode = "ready"
        if mode == "redirect":
            self.send_response(302)
            self.send_header("Location", "http://example.invalid/readyz")
            self.end_headers()
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, *_args):
        pass

class ReusableHTTPServer(http.server.ThreadingHTTPServer):
    allow_reuse_address = True

signal.signal(signal.SIGTERM, lambda _signal, _frame: os._exit(0))

for attempt in range(100):
    try:
        server = ReusableHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler)
        break
    except OSError:
        if attempt == 99:
            raise
        time.sleep(0.01)
with open(sys.argv[4], "w", encoding="ascii") as target:
    target.write(str(os.getpid()) + "\n")
with open(sys.argv[2], "w", encoding="ascii") as target:
    target.write("ready\n")
server.serve_forever()
PY
  pid=$!
  printf '%s\n' "${pid}" >"${NETNS_HTTP_PID_STATE}"
  for ((attempt = 0; attempt < 100; attempt++)); do
    if [[ -s "${NETNS_HTTP_READY_STATE}" && -s "${NETNS_HTTP_CHILD_PID_STATE}" ]]; then
      cp "${NETNS_HTTP_CHILD_PID_STATE}" "${NETNS_HTTP_PID_STATE}"
      return
    fi
    kill -0 "${pid}" 2>/dev/null || break
    /bin/sleep 0.01
  done
  remove_netns
  return 1
}

remove_netns() {
  local pid attempt
  if [[ -f "${NETNS_HTTP_PID_STATE}" ]]; then
    pid="$(cat "${NETNS_HTTP_PID_STATE}")"
    kill -TERM "${pid}" 2>/dev/null || true
    for ((attempt = 0; attempt < 100; attempt++)); do
      kill -0 "${pid}" 2>/dev/null || break
      /bin/sleep 0.01
    done
    if kill -0 "${pid}" 2>/dev/null; then
      kill -KILL "${pid}" 2>/dev/null || true
      for ((attempt = 0; attempt < 100; attempt++)); do
        kill -0 "${pid}" 2>/dev/null || break
        /bin/sleep 0.01
      done
    fi
  fi
  rm -f "${NETNS_HTTP_CHILD_PID_STATE}"
  rm -f "${NETNS_APP_STATE}" "${NETNS_WORKER_STATE}" "${NETNS_NETWORK_STATE}" \
    "${NETNS_PORT_STATE}" "${NETNS_HTTP_PID_STATE}" "${NETNS_HTTP_CHILD_PID_STATE}" "${NETNS_HTTP_READY_STATE}" \
    "${NETNS_HTTP_MODE_STATE}" "${NETNS_SECURITY_DRIFT_STATE}" \
    "${NETNS_APP_IMAGE_STATE}" "${NETNS_WORKER_IMAGE_STATE}"
}

ensure_desired_images() {
  [[ -f "${DESIRED_APP_IMAGE_STATE}" ]] || printf 'sha256:fake-app-v1\n' >"${DESIRED_APP_IMAGE_STATE}"
  [[ -f "${DESIRED_WORKER_IMAGE_STATE}" ]] || printf 'sha256:fake-worker-v1\n' >"${DESIRED_WORKER_IMAGE_STATE}"
}

emit_legacy_model_container() {
  local status=exited
  [[ -f "${LEGACY_MODEL_RUNNING_STATE}" ]] && status=running
  python3 - "${status}" <<'PY'
import json
import os
import sys

item = {
    "Name": "/zhixu-eino-live-ollama",
    "Image": "sha256:" + "a" * 64,
    "Config": {
        "Image": "ollama/ollama:0.9.6",
        "Labels": {},
        "User": "",
        "Entrypoint": ["/bin/ollama"],
        "Cmd": ["serve"],
        "ExposedPorts": {"11434/tcp": {}},
        "Volumes": {"/root/.ollama": {}},
        "Env": [
            "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
            "LD_LIBRARY_PATH=/usr/local/nvidia/lib:/usr/local/nvidia/lib64",
            "NVIDIA_VISIBLE_DEVICES=all",
            "NVIDIA_DRIVER_CAPABILITIES=compute,utility",
        ],
    },
    "HostConfig": {
        "NetworkMode": "default",
        "Privileged": False,
        "ReadonlyRootfs": False,
        "AutoRemove": False,
        "RestartPolicy": {"Name": "no", "MaximumRetryCount": 0},
        "PortBindings": {"11434/tcp": [{"HostIp": "127.0.0.1", "HostPort": "11434"}]},
        "Binds": None,
        "Devices": None,
        "DeviceRequests": None,
        "CapAdd": None,
        "CapDrop": None,
        "SecurityOpt": None,
        "ExtraHosts": None,
        "Links": None,
        "VolumesFrom": None,
    },
    "State": {"Status": sys.argv[1]},
    "NetworkSettings": {"Networks": {"bridge": {}}},
    "Mounts": [{
        "Type": "volume",
        "Name": "zhixu-eino-live-models",
        "Destination": "/root/.ollama",
        "RW": True,
    }],
}
if os.environ.get("ZHIXU_FAKE_LEGACY_MODEL_SHAPE_DRIFT") == "1":
    item["HostConfig"]["Privileged"] = True
print(json.dumps([item], separators=(",", ":")))
PY
}

emit_model_volume() {
  local name=$1
  python3 - "${name}" <<'PY'
import json
import os
import sys

name = sys.argv[1]
labels = {}
if name == "zhixu_zhixu-local-models":
    labels = {
        "com.docker.compose.project": "zhixu",
        "com.docker.compose.volume": "zhixu-local-models",
        "com.zhixu.owner": "local-model-runtime",
        "com.zhixu.schema": "local-model-store/v1",
    }
    if os.environ.get("ZHIXU_FAKE_MANAGED_MODEL_VOLUME_DRIFT") == "1":
        labels["com.zhixu.owner"] = "foreign"
item = {"Name": name, "Driver": "local", "Scope": "local", "Labels": labels, "Options": {}}
print(json.dumps([item], separators=(",", ":")))
PY
}

emit_legacy_tags() {
  python3 - <<'PY'
import json

models = [
    ("all-minilm:latest", "1" * 64, 45960996),
    ("qwen2.5:0.5b", "2" * 64, 397821000),
    ("qwen2.5:1.5b", "3" * 64, 986000000),
    ("qwen2.5:3b", "4" * 64, 1900000000),
]
print(json.dumps({"models": [{"name": name, "model": name, "digest": "sha256:" + digest, "size": size} for name, digest, size in models]}, separators=(",", ":")))
PY
}

emit_migration_verification() {
  python3 - <<'PY'
import base64
import json

models = [
    ("all-minilm:latest", "1" * 64, 45960996),
    ("qwen2.5:0.5b", "2" * 64, 397821000),
    ("qwen2.5:1.5b", "3" * 64, 986000000),
    ("qwen2.5:3b", "4" * 64, 1900000000),
]
payloads = {
    "version_base64": {"version": "0.32.9"},
    "tags_base64": {"models": [{"name": name, "model": name, "digest": "sha256:" + digest, "size": size} for name, digest, size in models]},
    "chat_base64": {"model": "qwen2.5:0.5b", "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}}]},
    "embedding_base64": {"model": "all-minilm:latest", "embeddings": [[0.1, 0.2, 0.3]]},
}
for key, value in payloads.items():
    encoded = base64.b64encode(json.dumps(value, separators=(",", ":")).encode()).decode()
    print(f"{key}={encoded}")
PY
}

emit_netns_inspect() {
  local port app_image worker_image include_app=$1 include_worker=$2
  port="$(netns_port)"
  app_image="$(cat "${NETNS_APP_IMAGE_STATE}")"
  worker_image="$(cat "${NETNS_WORKER_IMAGE_STATE}")"
  python3 - "${port}" "${app_image}" "${worker_image}" "${include_app}" "${include_worker}" <<'PY'
import json
import os
import sys

port, app_image, worker_image, include_app, include_worker = sys.argv[1:]

def item(name, service, image, user, entrypoint, caps, ports, tmpfs, healthcheck):
    return {
        "Name": "/" + name,
        "Image": image,
        "Config": {
            "User": user,
            "Entrypoint": entrypoint,
            "Healthcheck": {
                "Test": healthcheck,
                "Interval": 5000000000,
                "Timeout": 3000000000,
                "Retries": 20,
            },
            "Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],
            "Labels": {
                "com.docker.compose.project": "zhixu-netns",
                "com.docker.compose.service": service,
            },
        },
        "HostConfig": {
            "NetworkMode": "zhixu-runtime",
            "Privileged": False,
            "ReadonlyRootfs": True,
            "SecurityOpt": ["no-new-privileges:true"],
            "CapDrop": ["ALL"],
            "CapAdd": caps,
            "RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0},
            "PortBindings": ports,
            "ExtraHosts": ["host.docker.internal:host-gateway"],
            "Binds": None,
            "Tmpfs": tmpfs,
        },
        "State": {"Status": "running", "Health": {"Status": "healthy"}},
        "NetworkSettings": {"Networks": {"zhixu-runtime": {}}},
        "Mounts": [],
    }

items = []
if include_app == "1":
    items.append(item(
        "zhixu-app-netns", "app-netns", app_image, "0:0", ["/app/netns-ingress.sh"],
        ["CAP_NET_ADMIN", "CAP_SETUID", "CAP_SETGID"],
        {"8080/tcp": [{"HostIp": "127.0.0.1", "HostPort": port}]},
        {"/run": "rw,noexec,nosuid,size=65536"},
        ["CMD-SHELL", "ss -H -ltn 'sport = :8080' | grep -q '0.0.0.0:8080'"],
    ))
if include_worker == "1":
    items.append(item(
        "zhixu-worker-netns", "worker-netns", worker_image, "10001:10001",
        ["/app/netns-ingress.sh", "--worker-sentinel"],
        [], {},
        None,
        ["CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:18082"],
    ))
if os.environ.get("ZHIXU_FAKE_NETNS_CONFIG_DRIFT") == "1" and items:
    items[0]["HostConfig"]["Privileged"] = True
print(json.dumps(items, separators=(",", ":")))
PY
}

ensure_desired_images

printf 'ZHIXU_MODEL_SETTINGS_ROLLOUT_ID=%s ZHIXU_MODEL_SETTINGS_PREPARED=%s ZHIXU_APP_RESTART_POLICY=%s ZHIXU_WORKER_RESTART_POLICY=%s %s\n' \
  "${ZHIXU_MODEL_SETTINGS_ROLLOUT_ID:-}" "${ZHIXU_MODEL_SETTINGS_PREPARED:-}" \
  "${ZHIXU_APP_RESTART_POLICY:-}" "${ZHIXU_WORKER_RESTART_POLICY:-}" "$*" >>"${ZHIXU_FAKE_DOCKER_LOG}"

arguments=" $* "

if [[ "${1:-}" == "inspect" ]]; then
  case "${arguments}" in
    *" --type container zhixu-eino-live-ollama "*)
      [[ -f "${LEGACY_MODEL_CONTAINER_STATE}" ]] || exit 1
      emit_legacy_model_container
      ;;
    *" --format "*"zhixu-app-netns"*)
      [[ -f "${NETNS_APP_STATE}" ]] || exit 1
      if [[ "${arguments}" == *"com.docker.compose.project"* ]]; then
        printf 'zhixu-netns\tapp-netns\n'
      elif [[ "${arguments}" == *"com.docker.compose.service"* ]]; then
        printf 'app-netns\n'
      fi
      ;;
    *" --format "*"zhixu-worker-netns"*)
      [[ -f "${NETNS_WORKER_STATE}" ]] || exit 1
      if [[ "${arguments}" == *"com.docker.compose.project"* ]]; then
        printf 'zhixu-netns\tworker-netns\n'
      elif [[ "${arguments}" == *"com.docker.compose.service"* ]]; then
        printf 'worker-netns\n'
      fi
      ;;
    *" --format "*"fake-app-netns-id"*)
      printf '%s\n' '/zhixu-app-netns\tapp-netns'
      ;;
    *" --format "*"fake-worker-netns-id"*)
      printf '%s\n' '/zhixu-worker-netns\tworker-netns'
      ;;
    *" --format "*"fake-unknown-netns-id"*)
      printf '%s\n' '/zhixu-unknown-netns\tunknown-netns'
      ;;
    *" zhixu-app-netns zhixu-worker-netns "*)
      netns_exists || exit 1
      emit_netns_inspect 1 1
      ;;
    *" zhixu-app-netns "*)
      [[ -f "${NETNS_APP_STATE}" ]] || exit 1
      emit_netns_inspect 1 0
      ;;
    *" zhixu-worker-netns "*)
      [[ -f "${NETNS_WORKER_STATE}" ]] || exit 1
      emit_netns_inspect 0 1
      ;;
    *) exit 1 ;;
  esac
  exit 0
fi

if [[ "${1:-}" == "image" && "${2:-}" == "inspect" ]]; then
  case "${arguments}" in
    *" --format {{.Id}} zhixu-local-model-runtime "*) printf '%s\n' "${MANAGED_MODEL_IMAGE_ID}" ;;
    *" --format {{json .RepoDigests}} ${LEGACY_MODEL_IMAGE_ID} "*)
      printf '["ollama/ollama@sha256:f478761c18fea69b1624e095bce0f8aab06825d09ccabcd0f88828db0df185ce"]\n'
      ;;
    *" zhixu-netns-app-netns "*) cat "${DESIRED_APP_IMAGE_STATE}" ;;
    *" zhixu-netns-worker-netns "*) cat "${DESIRED_WORKER_IMAGE_STATE}" ;;
    *) exit 1 ;;
  esac
  exit 0
fi

if [[ "${1:-}" == "exec" ]]; then
  if [[ "${arguments}" == *" zhixu-eino-live-ollama "* ]]; then
    [[ -f "${LEGACY_MODEL_CONTAINER_STATE}" && -f "${LEGACY_MODEL_RUNNING_STATE}" ]] || exit 1
    case "${arguments}" in
      *" /api/version "*) printf '{"version":"0.9.6"}\n' ;;
      *" /api/tags "*) emit_legacy_tags ;;
      *) exit 1 ;;
    esac
    exit 0
  fi
  netns_exists || exit 1
  if [[ -f "${NETNS_SECURITY_DRIFT_STATE}" ]]; then
    printf 'Uid:\t0\t0\t0\t0\n'
    printf 'Gid:\t0\t0\t0\t0\n'
    printf 'CapPrm:\t0000000000000001\nCapEff:\t0000000000000001\nCapAmb:\t0000000000000000\nNoNewPrivs:\t0\n'
  else
    printf 'Uid:\t10001\t10001\t10001\t10001\n'
    printf 'Gid:\t10001\t10001\t10001\t10001\n'
    printf 'CapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\n'
  fi
  exit 0
fi

if [[ "${1:-}" == "ps" && "${arguments}" == *"label=com.docker.compose.project=zhixu-netns"* ]]; then
  [[ -f "${NETNS_APP_STATE}" ]] && printf 'fake-app-netns-id\n'
  [[ -f "${NETNS_WORKER_STATE}" ]] && printf 'fake-worker-netns-id\n'
  [[ "${ZHIXU_FAKE_NETNS_UNKNOWN_SERVICE:-0}" == "1" ]] && printf 'fake-unknown-netns-id\n'
  exit 0
fi

if [[ "${1:-}" == "ps" && "${arguments}" == *"label=com.docker.compose.project=zhixu"* ]]; then
  exit 0
fi

if [[ "${1:-}" == "ps" && "${arguments}" == *"volume=zhixu-eino-live-models"* ]]; then
  [[ -f "${LEGACY_MODEL_CONTAINER_STATE}" ]] && printf 'zhixu-eino-live-ollama\n'
  exit 0
fi

if [[ "${1:-}" == "ps" && "${arguments}" == *"volume=zhixu_zhixu-local-models"* ]]; then
  [[ -n "${ZHIXU_FAKE_MANAGED_MODEL_VOLUME_REFERENCE:-}" ]] \
    && printf '%s\n' "${ZHIXU_FAKE_MANAGED_MODEL_VOLUME_REFERENCE}"
  exit 0
fi

if [[ "${1:-}" == "ps" && "${arguments}" == *"publish=11434"* ]]; then
  [[ -f "${LEGACY_MODEL_CONTAINER_STATE}" && -f "${LEGACY_MODEL_RUNNING_STATE}" ]] \
    && printf 'zhixu-eino-live-ollama\n'
  exit 0
fi

if [[ "${1:-}" == "stop" ]]; then
  if [[ "${arguments}" == *" zhixu-eino-live-ollama "* ]]; then
    [[ -f "${LEGACY_MODEL_CONTAINER_STATE}" ]] || exit 1
    rm -f "${LEGACY_MODEL_RUNNING_STATE}"
  fi
  exit 0
fi

if [[ "${1:-}" == "start" && "${2:-}" == "zhixu-eino-live-ollama" ]]; then
  [[ -f "${LEGACY_MODEL_CONTAINER_STATE}" ]] || exit 1
  : >"${LEGACY_MODEL_RUNNING_STATE}"
  exit 0
fi

if [[ "${1:-}" == "rm" ]]; then
  case "${arguments}" in
    *" zhixu-eino-live-ollama "*) rm -f "${LEGACY_MODEL_CONTAINER_STATE}" "${LEGACY_MODEL_RUNNING_STATE}" ;;
    *" fake-app-netns-id "*) rm -f "${NETNS_APP_STATE}" ;;
    *" fake-worker-netns-id "*) rm -f "${NETNS_WORKER_STATE}" ;;
  esac
  exit 0
fi

if [[ "${1:-}" == "network" ]]; then
  case "${2:-}" in
    inspect)
      [[ -f "${NETNS_NETWORK_STATE}" ]] || exit 1
      if [[ "${arguments}" == *"{{.Name}}"* && "${arguments}" == *"com.docker.compose.project"* ]]; then
        printf '%s\n' 'zhixu-runtime\tzhixu-netns\tdefault'
      elif [[ "${arguments}" == *"{{.Name}}"* ]]; then
        printf 'zhixu-runtime\n'
      elif [[ "${arguments}" == *"com.docker.compose.project"* ]]; then
        printf 'zhixu-netns\tdefault\n'
      elif [[ "${arguments}" == *"len .Containers"* ]]; then
        count=0
        [[ -f "${NETNS_APP_STATE}" ]] && count=$((count + 1))
        [[ -f "${NETNS_WORKER_STATE}" ]] && count=$((count + 1))
        printf '%s\n' "${count}"
      else
        python3 - <<'PY'
import json
import os

network = {
    "Name": "zhixu-runtime",
    "Driver": "bridge",
    "Scope": "local",
    "Internal": False,
    "Attachable": False,
    "Ingress": False,
    "EnableIPv6": False,
    "Labels": {
        "com.docker.compose.project": "zhixu-netns",
        "com.docker.compose.network": "default",
    },
}
if os.environ.get("ZHIXU_FAKE_NETNS_NETWORK_DRIFT") == "1":
    network["Internal"] = True
print(json.dumps([network], separators=(",", ":")))
PY
      fi
      ;;
    ls)
      if [[ "${arguments}" == *"label=com.docker.compose.project=zhixu-netns"* && -f "${NETNS_NETWORK_STATE}" ]]; then
        printf 'fake-netns-network-id\n'
      fi
      ;;
    rm)
      rm -f "${NETNS_NETWORK_STATE}" "${NETNS_PORT_STATE}"
      ;;
    *) exit 1 ;;
  esac
  exit 0
fi

if [[ "${1:-}" == "volume" ]]; then
  volume_target="${@: -1}"
  [[ -z "${ZHIXU_FAKE_VOLUME_API_EXIT:-}" ]] || exit "${ZHIXU_FAKE_VOLUME_API_EXIT}"
  case "${2:-}" in
    inspect)
      case "${volume_target}" in
        zhixu-eino-live-models)
          [[ -f "${LEGACY_MODEL_VOLUME_STATE}" ]] || exit 1
          if [[ "${arguments}" == *" --format "* ]]; then
            printf 'zhixu-eino-live-models\t\t\n'
          else
            emit_model_volume zhixu-eino-live-models
          fi
          ;;
        zhixu_zhixu-local-models)
          [[ -f "${MANAGED_MODEL_VOLUME_STATE}" ]] || exit 1
          if [[ "${arguments}" == *" --format "* ]]; then
            if [[ "${arguments}" == *"com.zhixu.owner"* ]]; then
              if [[ "${ZHIXU_FAKE_MANAGED_MODEL_VOLUME_DRIFT:-0}" == 1 ]]; then
                printf 'foreign\tlocal-model-store/v1\n'
              else
                printf 'local-model-runtime\tlocal-model-store/v1\n'
              fi
            else
              printf 'zhixu_zhixu-local-models\tzhixu\tzhixu-local-models\n'
            fi
          else
            emit_model_volume zhixu_zhixu-local-models
          fi
          ;;
        *) exit 1 ;;
      esac
      ;;
    create)
      [[ "${arguments}" == *" zhixu_zhixu-local-models "* ]] || exit 1
      : >"${MANAGED_MODEL_VOLUME_STATE}"
      printf 'zhixu_zhixu-local-models\n'
      ;;
    ls)
      [[ -f "${MANAGED_MODEL_VOLUME_STATE}" && "${arguments}" == *"label=com.docker.compose.project=zhixu"* ]] \
        && printf 'zhixu_zhixu-local-models\n'
      ;;
    rm)
      if [[ "${arguments}" == *" zhixu_zhixu-local-models "* ]]; then
        rm -f "${MANAGED_MODEL_VOLUME_STATE}" "${MANAGED_MODEL_MIGRATION_STATE}"
      fi
      ;;
    *) exit 1 ;;
  esac
  exit 0
fi

if [[ "${1:-}" == "run" && "${arguments}" == *"/usr/local/bin/local-model-legacy-migrate"* ]]; then
  case "${arguments}" in
    *" source-preflight "*)
      [[ -f "${LEGACY_MODEL_VOLUME_STATE}" ]] || exit 1
      [[ "${ZHIXU_FAKE_LEGACY_MODEL_SPACE_EXIT:-0}" == 0 ]] || exit "${ZHIXU_FAKE_LEGACY_MODEL_SPACE_EXIT}"
      printf 'source_kib=4096\navailable_kib=1048576\nrequired_kib=69632\n'
      ;;
    *" source-fingerprint "*)
      if [[ "${ZHIXU_FAKE_LEGACY_MODEL_TREE_DRIFT:-0}" == 1 && ! -f "${LEGACY_MODEL_RUNNING_STATE}" ]]; then
        printf 'sha256:%064d\n' 9
      else
        printf 'sha256:%064d\n' 5
      fi
      ;;
    *" destination-fingerprint "*) printf 'sha256:%064d\n' 5 ;;
    *" destination-state "*)
      [[ -f "${MANAGED_MODEL_VOLUME_STATE}" ]] || exit 1
      case "$(cat "${MANAGED_MODEL_MIGRATION_STATE}" 2>/dev/null || true)" in
        copied) printf 'copied\n' ;;
        verified) printf 'verified\n' ;;
        foreign) exit 1 ;;
        *) printf 'fresh\n' ;;
      esac
      ;;
    *" copy "*)
      [[ "${ZHIXU_FAKE_LEGACY_MODEL_COPY_EXIT:-0}" == 0 ]] || exit "${ZHIXU_FAKE_LEGACY_MODEL_COPY_EXIT}"
      printf 'copied\n' >"${MANAGED_MODEL_MIGRATION_STATE}"
      ;;
    *" verify "*)
      [[ "${ZHIXU_FAKE_LEGACY_MODEL_VERIFY_EXIT:-0}" == 0 ]] || exit "${ZHIXU_FAKE_LEGACY_MODEL_VERIFY_EXIT}"
      emit_migration_verification
      ;;
    *" mark-verified "*) printf 'verified\n' >"${MANAGED_MODEL_MIGRATION_STATE}" ;;
    *" completed "*)
      if [[ "${ZHIXU_FAKE_LEGACY_MODEL_COMPLETED_MISMATCH:-0}" != 0 \
        || "$(cat "${MANAGED_MODEL_MIGRATION_STATE}" 2>/dev/null || true)" != verified ]]; then
        exit 1
      fi
      ;;
    *) exit 1 ;;
  esac
  exit 0
fi

if [[ "${1:-}" == "run" && "${arguments}" == *"/usr/local/bin/local-model-volume-init"* ]]; then
  [[ -f "${MANAGED_MODEL_VOLUME_STATE}" ]] || exit 1
  exit 0
fi

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
[[ "${action}" == "switch" || "${action}" == "reconcile" || "${action}" == "rebind" ]] || exit 62
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
workspace_id=""
expected_fingerprint=""
confirmation=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --workspace-root) root=$2; shift 2 ;;
    --workspace-id) workspace_id=$2; shift 2 ;;
    --expected-root-fingerprint) expected_fingerprint=$2; shift 2 ;;
    --confirm) confirmation=$2; shift 2 ;;
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
elif [[ "${action}" == "rebind" ]]; then
  [[ -n "${root}" && -n "${idempotency_key}" && "${workspace_id}" =~ ^[0-9a-f-]{36}$ \
    && "${expected_fingerprint}" =~ ^[0-9a-f]{64}$ && "${confirmation}" == "REBIND" \
    && "${initialize_git}" == "0" ]] || exit 64
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
elif [[ "${action}" == "rebind" ]]; then
  printf 'rebind --workspace-root %s --workspace-id %s --expected-root-fingerprint %s --idempotency-key present --confirm REBIND --compose-file %s --env-file %s --grant-override %s --compose-project %s --database-url-fd %s --control-instance-id present\n' \
    "${root}" "${workspace_id}" "${expected_fingerprint}" "${compose_file}" "${env_file}" \
    "${grant_override}" "${compose_project}" "${database_url_fd}" >>"${ZHIXU_FAKE_WORKSPACECTL_LOG}"
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
python3 - "${root}" "${grant_override}" "${action}" "$(dirname -- "${grant_override}")/workspace-selection" "${workspace_id}" <<'PY'
import hashlib
import json
import os
import sys
import uuid

root, grant_path, action, selection_path, requested_workspace_id = sys.argv[1:]
rebound_state_path = selection_path + ".fake-rebound"
response_loss_path = rebound_state_path + ".response-loss-consumed"
rebound_root = ""
if os.path.isfile(rebound_state_path):
    with open(rebound_state_path, "r", encoding="utf-8") as source:
        rebound_root = source.read()
already_rebound = rebound_root == root
workspace_id = requested_workspace_id or os.environ.get("ZHIXU_FAKE_WORKSPACECTL_WORKSPACE_ID") or str(
    uuid.uuid5(uuid.NAMESPACE_URL, "zhixu-workspace:" + root)
)
fingerprint_prefix = "zhixu-rebound-root:" if action == "rebind" or already_rebound else "zhixu-root:"
fingerprint = os.environ.get("ZHIXU_FAKE_WORKSPACECTL_FINGERPRINT") or hashlib.sha256(
    (fingerprint_prefix + root).encode("utf-8")
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
if action != "rebind":
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
    "binding_version": 2 if already_rebound else 1,
}
if action == "switch" and os.environ.get("ZHIXU_FAKE_WORKSPACECTL_SWITCH_BINDING_VERSION"):
    result["binding_version"] = int(os.environ["ZHIXU_FAKE_WORKSPACECTL_SWITCH_BINDING_VERSION"])
if action == "rebind":
    descriptor = os.open(rebound_state_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        os.write(descriptor, root.encode("utf-8"))
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    result.update({
        "status": "reconciled" if already_rebound else "rebound",
        "changed": not already_rebound,
        "binding_version": 2,
    })
    if os.environ.get("ZHIXU_FAKE_WORKSPACECTL_REBIND_RESPONSE_LOSS_ONCE") == "1" \
            and not os.path.isfile(response_loss_path):
        descriptor = os.open(response_loss_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        os.close(descriptor)
        raise SystemExit(44)
elif action == "switch" and changed:
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
  *" --project-name zhixu-netns "*" build app-netns worker-netns "*)
    if [[ -n "${ZHIXU_FAKE_NETNS_BUILD_ID:-}" ]]; then
      printf 'sha256:fake-app-%s\n' "${ZHIXU_FAKE_NETNS_BUILD_ID}" >"${DESIRED_APP_IMAGE_STATE}"
      printf 'sha256:fake-worker-%s\n' "${ZHIXU_FAKE_NETNS_BUILD_ID}" >"${DESIRED_WORKER_IMAGE_STATE}"
    fi
    ;;
  *"compose.netns.yml"*" config --format json "*)
    cat "${ZHIXU_FAKE_NETNS_COMPOSE_MODEL}"
    ;;
  *" --project-name zhixu-netns "*" up --detach "*)
    create_netns
    ;;
  *" --project-name zhixu-netns "*" down "*)
    remove_netns
    ;;
  *" --project-name zhixu-netns "*" ps --all --format json app-netns worker-netns "*)
    if netns_exists && [[ "${ZHIXU_FAKE_NETNS_READY:-1}" == "1" ]]; then
      printf '[{"Service":"app-netns","State":"running","Health":"healthy"},{"Service":"worker-netns","State":"running","Health":"healthy"}]\n'
    else
      printf '[]\n'
    fi
    ;;
  *" --project-name zhixu-netns "*" ps --all "*)
    if netns_exists; then
      printf 'NAME STATUS\nzhixu-app-netns Up (healthy)\nzhixu-worker-netns Up (healthy)\n'
    else
      printf 'NAME STATUS\n'
    fi
    ;;
  *"compose.static-models.yml"*" config --format json "*)
    cat "${ZHIXU_FAKE_STATIC_COMPOSE_MODEL}"
    ;;
  *"compose.bootstrap.yml"*" config --format json "*)
    cat "${ZHIXU_FAKE_BOOTSTRAP_COMPOSE_MODEL}"
    ;;
  *" config --format json "*)
    cat "${ZHIXU_FAKE_MANAGED_COMPOSE_MODEL}"
    ;;
  *" config --quiet "*)
    ;;
  *" port postgres 5432 "*)
    printf '%s\n' "${ZHIXU_FAKE_POSTGRES_ENDPOINT:-127.0.0.1:49123}"
    ;;
  *" ps --format json postgres local-model-runtime app worker app-model-relay worker-model-relay "*)
    if [[ "${ZHIXU_FAKE_RUNTIME_READY:-1}" == "1" ]]; then
      printf '[{"Service":"postgres","State":"running","Health":"%s"},{"Service":"local-model-runtime","State":"running","Health":"healthy"},{"Service":"app","State":"running","Health":"healthy"},{"Service":"worker","State":"running","Health":"healthy"},{"Service":"app-model-relay","State":"running","Health":"healthy"},{"Service":"worker-model-relay","State":"running","Health":"healthy"}]\n' \
        "${ZHIXU_FAKE_POSTGRES_HEALTH:-healthy}"
    else
      printf '[{"Service":"postgres","State":"running","Health":"healthy"},{"Service":"local-model-runtime","State":"running","Health":"starting"},{"Service":"app","State":"running","Health":"starting"},{"Service":"worker","State":"running","Health":"starting"},{"Service":"app-model-relay","State":"running","Health":"starting"},{"Service":"worker-model-relay","State":"running","Health":"starting"}]\n'
    fi
    ;;
  *" ps --all --format json postgres local-model-runtime app worker app-model-relay worker-model-relay "*)
    if [[ "${ZHIXU_FAKE_STATUS_READY:-1}" == "1" ]]; then
      printf '[{"Service":"postgres","State":"running","Health":"%s"},{"Service":"local-model-runtime","State":"running","Health":"healthy"},{"Service":"app","State":"running","Health":"healthy"},{"Service":"worker","State":"running","Health":"healthy"},{"Service":"app-model-relay","State":"running","Health":"healthy"},{"Service":"worker-model-relay","State":"running","Health":"healthy"}]\n' \
        "${ZHIXU_FAKE_POSTGRES_HEALTH:-healthy}"
    else
      printf '[{"Service":"postgres","State":"running","Health":"healthy"},{"Service":"local-model-runtime","State":"running","Health":"healthy"},{"Service":"app","State":"running","Health":"healthy"},{"Service":"worker","State":"running","Health":"healthy"},{"Service":"app-model-relay","State":"exited","Health":"unhealthy"},{"Service":"worker-model-relay","State":"running","Health":"healthy"}]\n'
    fi
    ;;
  *" ps --all "*)
    printf 'NAME STATUS\nzhixu-app-1 Up\nzhixu-app-model-relay-1 Exited\nzhixu-worker-model-relay-1 Up\n'
    ;;
  *" run --rm --no-deps -T model-settings-key-init "*)
    [[ "${ZHIXU_FAKE_KEY_INIT_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_KEY_INIT_EXIT}"
    ;;
  *" run --rm --no-deps -T migrate "*)
    [[ "${ZHIXU_FAKE_MIGRATE_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_MIGRATE_EXIT}"
    ;;
  *" run --rm --no-deps -T local-model-runtime-credential-init "*)
    [[ "${ZHIXU_FAKE_LOCAL_MODEL_RUNTIME_CREDENTIAL_INIT_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_LOCAL_MODEL_RUNTIME_CREDENTIAL_INIT_EXIT}"
    ;;
  *" run --rm --no-deps -T local-model-volume-init "*)
    [[ "${ZHIXU_FAKE_LOCAL_MODEL_VOLUME_INIT_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_LOCAL_MODEL_VOLUME_INIT_EXIT}"
    ;;
  *" run --rm --no-deps -T modelctl recover --stale "*)
    [[ "${ZHIXU_FAKE_MODELCTL_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_MODELCTL_EXIT}"
    ;;
  *" up --detach --wait local-model-runtime "*)
    [[ "${ZHIXU_FAKE_LOCAL_MODEL_RUNTIME_START_EXIT:-0}" == "0" ]] || exit "${ZHIXU_FAKE_LOCAL_MODEL_RUNTIME_START_EXIT}"
    ;;
esac
