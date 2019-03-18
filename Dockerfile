FROM golang:alpine AS builder

ADD . /go/src/github.com/foosinn/minihub

WORKDIR /go/src/github.com/foosinn/minihub

RUN true \
  && apk add --no-cache git binutils \
  && go get github.com/rakyll/statik \
  && /go/bin/statik -src=$PWD/static -f \
  && go build minihub.go \
  && strip minihub

# ---

FROM alpine
RUN apk add --no-cache ca-certificates
COPY --from=builder /go/src/github.com/foosinn/minihub/minihub /minihub
CMD /minihub



