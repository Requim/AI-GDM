FROM mcr.microsoft.com/playwright:v1.55.0-noble@sha256:ffc33305f7b4b04057ae4a0caa70aad4fde87454fb403a1a22e7f931707dfcf9

ENV NPM_CONFIG_REGISTRY=https://registry.npmjs.org
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

WORKDIR /opt/e2e
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --fund=false \
    && npm cache clean --force

COPY playwright.config.js audit-results.mjs expected-tests.json ./
COPY specs ./specs

CMD ["sh", "-c", "./node_modules/.bin/playwright test --reporter=json > /tmp/security-report.json; status=$?; cat /tmp/security-report.json; [ $status -eq 0 ] || exit $status; node audit-results.mjs expected-tests.json /tmp/security-report.json"]
