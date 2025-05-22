#!/usr/bin/env bash
# k3s_join_token.sh - Enterprise Edge Node Bootstrap
# Security Compliance: CIS Kubernetes Benchmark, NIST 800-190

set -o errexit
set -o nounset
set -o pipefail
set -o errtrace
trap 'cleanup_and_fail' ERR

readonly MAX_RETRIES=3
readonly CLUSTER_API="${CLUSTER_API:-https://k3s-controlplane.cirium.ai:6443}"
declare -g TOKEN_FILE=""
declare -g AUDIT_LOG="/var/log/cirium/bootstrap.log"

main() {
  initialize_audit
  validate_privileges
  check_prerequisites
  verify_compatibility
  parse_arguments "$@"
  verify_join_token
  configure_os
  install_dependencies
  setup_firewall
  join_cluster
  deploy_monitoring
  finalize_audit
}

cleanup_and_fail() {
  local exit_code=$?
  log_audit_event "FAILURE" "Bootstrap failed with code $exit_code"
  [[ -f "$TOKEN_FILE" ]] && shred -u "$TOKEN_FILE" 2>/dev/null || true
  exit $exit_code
}

initialize_audit() {
  mkdir -p "$(dirname "$AUDIT_LOG")"
  exec 5>>"$AUDIT_LOG"
  log_audit_event "START" "Initiated join process"
}

log_audit_event() {
  local status=\$1
  local message=\$2
  echo "$(date -uIs) | $(hostname) | $status | $message" >&5
}

validate_privileges() {
  [[ $EUID -eq 0 ]] || {
    echo >&2 "Requires root privileges"
    log_audit_event "DENIED" "Non-root execution attempt"
    exit 126
  }
}

check_prerequisites() {
  declare -a required_utils=("curl" "tar" "grep" "shred")
  for util in "${required_utils[@]}"; do
    if ! command -v "$util" >/dev/null 2>&1; then
      echo >&2 "Missing required utility: $util"
      log_audit_event "PREREQ_FAIL" "Missing utility: $util"
      exit 127
    fi
  done
}

verify_compatibility() {
  local supported_os=("rhel" "centos" "rocky" "ubuntu" "debian")
  local os_id
  os_id=$(awk -F= '/^ID=/{print \$2}' /etc/os-release | tr -d '"')
  
  if ! printf "%s\n" "${supported_os[@]}" | grep -q "^${os_id}$"; then
    echo >&2 "Unsupported OS: $os_id"
    log_audit_event "INCOMPATIBLE" "Unsupported OS: $os_id"
    exit 128
  fi
}

parse_arguments() {
  while (($# > 0)); do
    case "\$1" in
      --token-file)
        TOKEN_FILE="\$2"
        shift 2
        ;;
      *)
        echo >&2 "Invalid argument: \$1"
        exit 128
        ;;
    esac
  done

  [[ -n "$TOKEN_FILE" && -f "$TOKEN_FILE" ]] || {
    echo >&2 "Valid --token-file required"
    exit 128
  }
}

verify_join_token() {
  local token
  token=$(<"$TOKEN_FILE")
  [[ "$token" =~ ^K[0-9a-f]{40}:: ]] || {
    echo >&2 "Invalid token format"
    log_audit_event "INVALID_TOKEN" "Token validation failed"
    exit 129
  }
  shred -u "$TOKEN_FILE"
}

configure_os() {
  echo "net.ipv4.conf.all.forwarding=1" > /etc/sysctl.d/99-nuzon.conf
  sysctl -p /etc/sysctl.d/99-nuzon.conf
}

install_dependencies() {
  local packages=("ipset" "iptables" "conntrack")
  if command -v apt-get >/dev/null; then
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -yqq "${packages[@]}"
  elif command -v yum >/dev/null; then
    yum install -y -q "${packages[@]}"
  fi
}

setup_firewall() {
  if command -v ufw >/dev/null; then
    ufw allow 6443/tcp comment "K3s API Server"
    ufw allow 8472/udp comment "Flannel VXLAN"
    ufw reload
  elif command -v firewall-cmd >/dev/null; then
    firewall-cmd --permanent --add-port=6443/tcp
    firewall-cmd --permanent --add-port=8472/udp
    firewall-cmd --reload
  fi
}

join_cluster() {
  local attempt=0
  local token
  token=$(<"$TOKEN_FILE")
  
  until ((attempt >= MAX_RETRIES)); do
    if curl -sfL https://get.k3s.io | \
      INSTALL_K3S_VERSION="v1.27.4+nuzon1" \
      K3S_URL="$CLUSTER_API" \
      K3S_TOKEN="$token" \
      INSTALL_K3S_EXEC="agent" \
      sh -s - \
        --node-label "nuzon.ai/edge=true" \
        --kubelet-arg="feature-gates=DevicePlugins=true" \
        --kubelet-arg="max-pods=250"; then
      
      log_audit_event "SUCCESS" "Cluster join completed"
      return 0
    fi
    ((attempt++)) || true
    sleep $((attempt * 10))
  done

  log_audit_event "JOIN_FAILURE" "Exhausted $MAX_RETRIES attempts"
  exit 130
}

deploy_monitoring() {
  local retry=0
  until kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml apply -f - <<EOF
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: nuzon-edge-monitor
spec:
  selector:
    matchLabels:
      app: edge-monitor
  template:
    metadata:
      labels:
        app: edge-monitor
    spec:
      containers:
      - name: node-exporter
        image: prom/node-exporter:v1.6.1
        ports:
        - containerPort: 9100
EOF
  do
    ((retry++)) || true
    ((retry >= 3)) && break
    sleep 10
  done
}

finalize_audit() {
  log_audit_event "COMPLETE" "Edge node operational"
  chmod 600 "$AUDIT_LOG"
}

main "$@"
