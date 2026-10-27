FROM golang:1.26-alpine AS api-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cctv-sub ./cmd/cctv-sub

FROM api-builder AS api-test
RUN apk add --no-cache ca-certificates ffmpeg
CMD ["go", "test", "./cmd/...", "./internal/..."]

FROM alpine:3.22 AS api
RUN apk add --no-cache ca-certificates ffmpeg tzdata
ENV TZ=Asia/Shanghai
WORKDIR /app
COPY --from=api-builder /out/cctv-sub /usr/local/bin/cctv-sub
ENTRYPOINT ["cctv-sub"]

FROM node:22-alpine AS web
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY .openai ./.openai
COPY app ./app
COPY build ./build
COPY public ./public
COPY worker ./worker
COPY next-env.d.ts next.config.ts postcss.config.mjs tsconfig.json vite.config.ts eslint.config.mjs ./
ARG NEXT_PUBLIC_API_URL=
ENV NEXT_PUBLIC_API_URL=$NEXT_PUBLIC_API_URL
RUN npm run build
ENV NODE_ENV=production PORT=3000
EXPOSE 3000
CMD ["npm", "run", "start", "--", "-H", "0.0.0.0", "-p", "3000"]
