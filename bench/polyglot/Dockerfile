# stubbs polyglot benchmark image: toolchains for python/go/javascript
# exercises from Aider-AI/polyglot-benchmark. Adapted from aider's own
# benchmark image (buildpack-deps:jammy + deadsnakes py3.11 + go1.21.5 +
# node20 with the exact jest/babel pins), minus java/rust/cpp/aider itself.
FROM buildpack-deps:jammy

RUN apt-get update && apt-get install -y software-properties-common \
    && add-apt-repository ppa:deadsnakes/ppa \
    && apt-get update && apt-get install -y \
        python3.11 python3.11-venv python3.11-dev python3-pip \
    && rm -rf /var/lib/apt/lists/*
RUN update-alternatives --install /usr/bin/python3 python3 /usr/bin/python3.11 1
RUN python3.11 -m pip install --no-cache-dir --upgrade pip \
    && python3.11 -m pip install --no-cache-dir pytest

RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "x86_64" ]; then GOARCH="amd64"; \
    elif [ "$ARCH" = "aarch64" ]; then GOARCH="arm64"; \
    else false; fi && \
    curl -fsSL "https://golang.org/dl/go1.21.5.linux-$GOARCH.tar.gz" -o /tmp/go.tar.gz && \
    tar -C /usr/local -xzf /tmp/go.tar.gz && rm /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /npm-install && cd /npm-install && npm init -y && \
    npm install --no-audit --no-fund \
        jest@29.7.0 \
        @babel/core@7.25.2 \
        @exercism/babel-preset-javascript@0.2.1 \
        @exercism/eslint-config-javascript@0.6.0 \
        @types/jest@29.5.12 \
        @types/node@20.12.12 \
        babel-jest@29.6.4 \
        core-js@3.37.1 \
        eslint@8.49.0
ENV NODE_PATH=/npm-install/node_modules

WORKDIR /work
