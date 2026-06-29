# build environment
FROM golang:1.26-alpine AS build
RUN apk add --update gcc musl-dev && \
    mkdir -vp /app
COPY ./go.mod /app/go.mod
COPY ./go.sum /app/go.sum
COPY ./api /app/api
COPY ./app /app/app
COPY ./common /app/common
COPY ./db /app/db
COPY ./models /app/models
COPY ./session /app/session
COPY ./main.go /app/main.go
RUN cd /app && \
    CGO_ENABLED=1 go build -o rest-pty . && \
    cp -v ./rest-pty /usr/bin/

# deploy environment
FROM alpine
COPY --from=build /usr/bin/rest-pty /usr/bin/
ENTRYPOINT ["/usr/bin/rest-pty"]
