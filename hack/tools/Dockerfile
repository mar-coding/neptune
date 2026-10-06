# Dev/tooling image for NEPTUNE: everything except Docker and kind runs here.
# Build:  docker build -t neptune-tools hack/tools
# Use:    hack/dock.sh <cmd>
FROM golang:1.22-bookworm

ARG KUBECTL_VERSION=v1.20.15
ARG HELM_VERSION=v3.15.4

RUN apt-get update \
    && apt-get install -y --no-install-recommends make git curl ca-certificates jq postgresql-client \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
    && chmod +x /usr/local/bin/kubectl \
    && curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz" | tar -xz -C /tmp \
    && mv /tmp/linux-amd64/helm /usr/local/bin/helm \
    && rm -rf /tmp/linux-amd64

WORKDIR /workspace
