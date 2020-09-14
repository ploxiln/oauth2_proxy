FROM golang:1.15-alpine

RUN apk update && apk add git

WORKDIR $GOPATH/src/github.com/ploxiln/oauth2_proxy/
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /usr/local/bin/oauth2_proxy


FROM busybox

EXPOSE 4180

# set up nsswitch.conf for Go's "netgo" implementation
# https://github.com/golang/go/issues/35305
RUN [ ! -e /etc/nsswitch.conf ] && echo 'hosts: files dns' > /etc/nsswitch.conf

COPY --from=0  /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=0  /usr/local/bin/oauth2_proxy        /usr/local/bin/

USER www-data
CMD ["oauth2_proxy"]
