#telegrafSrc="/Volumes/DATA/iprudnikov/DevNS/src/github.com/influxdata/telegraf"
docker run -it -v /Volumes/DATA/ilya.prudnikov/DevNS/src:/go/src --rm --workdir "/go/src/github.com/influxdata/telegraf" \
--env "GOPATH=/go" --env "GOOS=linux" --env "GOARCH=amd64" --env "CGO_ENABLED=0" \
golang:latest go build -ldflags "-X main.version=release.local -extldflags '-static'"  \
-v -o ./build/procstatDeb ./cmd/telegraf