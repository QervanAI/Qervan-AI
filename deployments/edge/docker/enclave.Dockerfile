# enclave.Dockerfile - Intel SGX Enclave Container Builder
# Build Stage
FROM ubuntu:22.04 AS builder

# Install SGX components
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    wget \
    gnupg && \
    wget -qO - https://download.01.org/intel-sgx/sgx_repo/ubuntu/intel-sgx-deb.key | apt-key add - && \
    echo 'deb [arch=amd64] https://download.01.org/intel-sgx/sgx_repo/ubuntu jammy main' > /etc/apt/sources.list.d/intel-sgx.list && \
    apt-get update && \
    echo yes | apt-get install -y \
    sgx-dcap-pccs \
    sgx-dcap-quote-verify \
    sgx-ae-qve \
    libsgx-dcap-default-qpl \
    libsgx-dcap-quote-verify-dev && \
    rm -rf /var/lib/apt/lists/*

# Copy enclave builder
COPY . /app
WORKDIR /app
RUN make SGX_MODE=HW SGX_DEBUG=0

# Runtime Stage  
FROM ubuntu:22.04

# Install runtime dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    libsgx-enclave-common \
    libsgx-dcap-quote-verify \
    libsgx-ae-qve \
    libsgx-quote-ex-dev && \
    rm -rf /var/lib/apt/lists/*

# Copy built artifacts
COPY --from=builder /app/enclave.signed.so /opt/Wavine/enclave/
COPY --from=builder /app/enclave_runner /usr/local/bin/

# Security hardening
RUN groupadd -r enclave && \
    useradd -r -g enclave -d /nonexistent -s /usr/sbin/nologin enclave && \
    chmod 755 /opt/nuzon/enclave && \
    chown -R enclave:enclave /opt/Wavine/enclave

USER enclave
EXPOSE 50051
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD ["enclave_runner", "healthcheck"]
ENTRYPOINT ["enclave_runner"]

# Metadata
LABEL org.opencontainers.image.title="Wavine SGX Enclave" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="${VCS_URL}" \
      ai.nuzon.sgx.mode="HW" \
      ai.nuzon.sgx.epid="enabled" \
      ai.nuzon.sgx.dcap="enabled"
