FROM golang:alpine AS builder

RUN \
    apk add --update git && \
    rm -rf /var/cache/apk/*

RUN addgroup -S gouser && adduser -S gouser -G gouser 

RUN mkdir -p /usr/local/go/src/todo
RUN mkdir -p /data
WORKDIR /usr/local/go/src/todo

COPY . /usr/local/go/src/todo

RUN go get -v -d
RUN go install -v
RUN go build .

FROM alpine:latest
# Copy CA certificates to be able to connect to HTTPS sites.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
 # COPY /etc/passwd and /etc/group to have the user in new image
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
ENV GOPATH=/usr/local/go
COPY --from=builder --chown=gouser:gouser /usr/local/go/src/todo/ /usr/local/go/src/todo
WORKDIR /usr/local/go/src/todo
# Drop privileges, don't run as root
USER gouser:gouser

EXPOSE 8000/tcp

ENTRYPOINT ["/usr/local/go/src/todo/todo"]
