FROM node:22-bookworm

ARG NPM_REGISTRY=https://registry.npmjs.org
ENV NPM_CONFIG_REGISTRY=${NPM_REGISTRY}
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

RUN sed -i 's|http://deb.debian.org/debian|https://mirrors.cloud.tencent.com/debian|g' \
      /etc/apt/sources.list.d/debian.sources \
    && apt-get -o Acquire::ForceIPv4=true update \
    && apt-get -o Acquire::ForceIPv4=true install -y --no-install-recommends ca-certificates chromium \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/e2e
COPY package.json ./
RUN npm install --no-package-lock --ignore-scripts \
    && npm cache clean --force

COPY playwright.config.js ./
COPY specs ./specs

ENV E2E_CHROMIUM_PATH=/usr/bin/chromium
CMD ["npm", "test"]
