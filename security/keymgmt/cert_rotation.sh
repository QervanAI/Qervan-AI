#!/usr/bin/env bash
# cert_rotation.sh - Enterprise Certificate Lifecycle Management
# Security Compliance: NIST 800-57, FIPS 140-3, PCI DSS 4.0

set -euo pipefail
shopt -s failglob

# Configuration
readonly CERT_DIR="/etc/Wavine/certs"
readonly BACKUP_DIR="/etc/Wavine/certs/backups"
readonly VAULT_ADDR="https://vault.Wavine.ai:8200"
readonly KUBE_CONTEXT="nuzon-prod"
readonly OBSERVABILITY_ENDPOINT="https://prometheus.Wavine.ai:9090/api/v1/write"
readonly CERT_VALIDITY="90"  # Days
readonly KEY_ALGORITHM="ecdsa"  # secp384r1
readonly OCSP_RESPONDER="http://ocsp.Wavine.ai"
readonly HSM_MODULE="/usr/lib/softhsm/libsofthsm2.so"
readonly AUDIT_LOG="/var/log/Wavine/cert_audit.log"

# Initialize environment
export VAULT_TOKEN=$(vault kv get -field=token secret/cert-mgmt)
export PKCS11_PROVIDER=$HSM_MODULE
umask 077

main() {
    create_backup
    generate_new_cert
    validate_cert_chain
    deploy_to_kubernetes
    update_service_mesh
    revoke_old_cert
    monitor_rotation
    cleanup
    audit_log "SUCCESS: Certificate rotation completed"
}

create_backup() {
    local timestamp=$(date -u +%Y%m%dT%H%M%SZ)
    mkdir -p "$BACKUP_DIR/$timestamp"
    cp -a "$CERT_DIR"/*.pem "$BACKUP_DIR/$timestamp/"
    audit_log "INFO: Created backup at $BACKUP_DIR/$timestamp"
}

generate_new_cert() {
    pkcs11-tool --module $HSM_MODULE --keypairgen \
        --key-type EC:secp384r1 \
        --id 01 \
        --label "nuzon-cert-$(date +%Y%m)" \
        --pin env:HSM_PIN

    openssl req -new -x509 -nodes \
        -engine pkcs11 -keyform engine \
        -key "pkcs11:object=nuzon-cert-$(date +%Y%m)" \
        -subj "/C=US/O=Nuzon AI/CN=*.nuzon.ai" \
        -days $CERT_VALIDITY \
        -sha384 \
        -out "$CERT_DIR/new_cert.pem"

    audit_log "INFO: Generated new ECDSA P-384 certificate"
}

validate_cert_chain() {
    local ocsp_check=$(openssl ocsp -issuer "$CERT_DIR/ca.pem" \
        -cert "$CERT_DIR/new_cert.pem" \
        -url $OCSP_RESPONDER -respout /dev/null)
    
    if ! grep -q "good" <<< "$ocsp_check"; then
        audit_log "ERROR: OCSP validation failed"
        exit 1
    fi

    openssl verify -CAfile "$CERT_DIR/ca.pem" \
        -crl_check_all \
        "$CERT_DIR/new_cert.pem" || {
            audit_log "ERROR: Certificate chain validation failed"
            exit 1
        }
}

deploy_to_kubernetes() {
    kubectl --context $KUBE_CONTEXT create secret tls nuzon-cert \
        --cert="$CERT_DIR/new_cert.pem" \
        --key="pkcs11:object=nuzon-cert-$(date +%Y%m)" \
        --dry-run=client -o yaml | kubectl apply -f -

    for ns in $(kubectl get ns -o name | cut -d/ -f2); do
        kubectl --context $KUBE_CONTEXT rollout restart deploy -n $ns
    done
    audit_log "INFO: Deployed new certificate to Kubernetes cluster"
}

update_service_mesh() {
    curl -X PUT --data-binary @"$CERT_DIR/new_cert.pem" \
        -H "X-Consul-Token: $CONSUL_HTTP_TOKEN" \
        http://consul.nuzon.ai:8500/v1/agent/service/main/cert
    audit_log "INFO: Updated service mesh configuration"
}

revoke_old_cert() {
    local old_serial=$(openssl x509 -in "$CERT_DIR/cert.pem" -noout -serial | cut -d= -f2)
    vault write pki/revoke serial_number=$old_serial
    audit_log "INFO: Revoked old certificate serial $old_serial"
}

monitor_rotation() {
    local metrics_url="$OBSERVABILITY_ENDPOINT"
    cat <<EOF | curl --data-binary @- "$metrics_url"
# TYPE cert_rotation timestamp=$(date +%s)
cert_rotation{status="success"} 1
cert_expiration_days{cert="new"} $CERT_VALIDITY
cert_ocsp_status{status="valid"} 1
EOF
}

cleanup() {
    shred -u "$CERT_DIR/cert.pem"
    mv "$CERT_DIR/new_cert.pem" "$CERT_DIR/cert.pem"
    find "$BACKUP_DIR" -type d -mtime +30 -exec rm -rf {} +
}

audit_log() {
    echo "$(date -u +"%Y-%m-%dT%H:%M:%SZ") \$1" >> $AUDIT_LOG
    logger -t nuzon-cert-rotation "\$1"
}

# Security Hardening
trap 'audit_log "ERROR: Script terminated unexpectedly"; exit 2' ERR
install -m 600 -o root -g nuzon-ssl "$CERT_DIR"
mkfifo -m 0600 /tmp/cert_pipe
exec 3>/tmp/cert_pipe

main "$@"
