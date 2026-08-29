FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /out/feishu-collector .

FROM alpine:3.20
COPY --from=build /out/feishu-collector /usr/local/bin/feishu-collector
ENTRYPOINT ["/usr/local/bin/feishu-collector", "--watch"]
