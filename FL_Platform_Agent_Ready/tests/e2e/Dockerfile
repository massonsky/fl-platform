FROM mcr.microsoft.com/playwright@sha256:f1e7e01021efd65dd1a2c56064be399f3e4de00fd021ac561325f2bfbb2b837a
RUN npm install --prefix /opt/browser-tools --no-audit --no-fund playwright@1.56.1
ENV NODE_PATH=/opt/browser-tools/node_modules
ENTRYPOINT ["node", "/src/tests/e2e/browser.cjs"]
