FROM golang:1.26.1-alpine AS backend-build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
WORKDIR /src/backend
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /server ./cmd/server

FROM alpine:3.23 AS backend
RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 gateway
WORKDIR /app
COPY --from=backend-build /server /app/server
COPY backend/configs/dev/config.yaml /app/backend/configs/dev/config.yaml
COPY deploy/config.prod.yaml /app/deploy/config.prod.yaml
RUN mkdir /app/data && chown gateway:gateway /app/data
USER gateway
EXPOSE 8080
ENTRYPOINT ["/app/server"]

FROM node:24-alpine AS web-build
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM nginx:1.28-alpine AS web
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=web-build /src/dist /usr/share/nginx/html
EXPOSE 80
