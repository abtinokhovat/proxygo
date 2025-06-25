# Build Stage
FROM golang:1.24.0-alpine AS build
RUN apk --no-cache add ca-certificates
RUN apk update && apk add git
RUN apk add --no-cache make

ENV GO111MODULE=on \
    GOOS=linux \
    GOARCH=amd64

WORKDIR /app
COPY . .

RUN make build

# Runtime Stage
FROM alpine:3.18
ENV TZ=UTC

RUN apk add --update --no-cache tzdata ca-certificates bash && \
    cp --remove-destination /usr/share/zoneinfo/${TZ} /etc/localtime && \
    echo "${TZ}" > /etc/timezone && \
    apk del tzdata

COPY --from=build /app/build/proxygo /bin/proxygo
COPY --from=build /etc/ssl/certs /etc/ssl/certs

# Run
CMD [ "/bin/proxygo"]

