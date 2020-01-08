docker run -it -v /Volumes/DATA/iprudnikov/DevNS/src:/telegraf --rm \
  --env "GOPATH=/go" --env "GOOS=linux" --env "GOARCH=amd64" --env "CGO_ENABLED=0" \
  golang:latest \
  env &&
  git clone https://github.com/go-delve/delve.git "${GOPATH}"/src/github.com/go-delve/delve &&
  cd "${GOPATH}"/src/github.com/go-delve/delve &&
  make install
# && \
#cp  /telegraf/github.com/influxdata/telegraf/plugins/inputs/docker_cnt_logs/deb/dlv
