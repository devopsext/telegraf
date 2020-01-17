#telegrafSrc="/Volumes/DATA/iprudnikov/DevNS/src/github.com/influxdata/telegraf"
docker run -it -v /Users/ilya.prudnikov/DevNS/src:/go/src --rm --workdir "/go/src/github.com/influxdata/telegraf" \
  --env "GOPATH=/go" --env "GOOS=linux" --env "GOARCH=amd64" --env "CGO_ENABLED=0" \
  golang:1.13 go build -gcflags "all=-N -l" -ldflags "-X main.version=release.local -extldflags '-static'" \
  -v -o ./build/procstatDeb ./cmd/telegraf
