FROM mcr.microsoft.com/playwright:v1.64.0-noble@sha256:06a9939e57531807f8d5fd76ce44b53165ffb7d7501d87ab10e285c20b1e971f
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
WORKDIR /opt/infogap-browser
RUN npm install --ignore-scripts --no-audit --no-fund --omit=dev playwright@1.64.0 \
    && npm cache clean --force
COPY scripts/browser-probe.mjs ./browser-probe.mjs
COPY data/sources.json data/x-accounts.json ./data/
RUN chmod 0555 /opt/infogap-browser /opt/infogap-browser/data \
    && chmod 0444 /opt/infogap-browser/browser-probe.mjs /opt/infogap-browser/data/*.json
USER pwuser
ENTRYPOINT ["xvfb-run", "-a", "node", "/opt/infogap-browser/browser-probe.mjs"]
CMD ["--probe", "home"]
