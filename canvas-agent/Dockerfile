FROM oven/bun:1.3.13

WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY src ./src
COPY tsconfig.json ./
RUN bun run build

ENV CANVAS_HOST=0.0.0.0
ENV PORT=17371
ENV CANVAS_AGENT_URL=http://127.0.0.1:17371

EXPOSE 17371
VOLUME ["/data"]

CMD ["node", "dist/index.js"]
