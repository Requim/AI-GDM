FROM node:22-bookworm

ARG NPM_REGISTRY=https://registry.npmjs.org
ARG CHROMIUM_VERSION=151.0.7922.173-1~deb12u1
ARG SOURCE_TREE_SHA=unknown
ENV NPM_CONFIG_REGISTRY=${NPM_REGISTRY}
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
LABEL org.opencontainers.image.revision=${SOURCE_TREE_SHA}

RUN sed -i 's|http://deb.debian.org/debian|https://mirrors.cloud.tencent.com/debian|g' \
      /etc/apt/sources.list.d/debian.sources \
    && apt-get -o Acquire::ForceIPv4=true update \
    && apt-get -o Acquire::ForceIPv4=true install -y --no-install-recommends \
      ca-certificates "chromium=${CHROMIUM_VERSION}" \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/e2e
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts \
    && npm cache clean --force

COPY playwright.config.js ./
COPY specs ./specs

ENV E2E_CHROMIUM_PATH=/usr/bin/chromium
CMD ["npm", "test"]
