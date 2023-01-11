FROM golang:1.19.4-alpine as build-env

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY . .
RUN go mod vendor

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /vpa-minallowed-remover

FROM gcr.io/distroless/base

WORKDIR /
COPY --from=build-env /vpa-minallowed-remover /vpa-minallowed-remover

EXPOSE 8080
USER nonroot

ENTRYPOINT ["/vpa-minallowed-remover"]