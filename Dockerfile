FROM golang:1.13-alpine as builder

ENV GO111MODULE=on
ENV CGO_ENABLED=0

RUN apk add --no-cache make git upx

WORKDIR /go/src/github.com/cloudflare/cloudflared/
COPY . .

ARG VERSION
ARG DATE

RUN make cloudflared
RUN upx cloudflared

FROM scratch
COPY --from=builder /go/src/github.com/cloudflare/cloudflared/cloudflared /usr/local/bin/

ENV NO_AUTOUPDATE=true

ENTRYPOINT ["cloudflared"]
CMD ["version"]
