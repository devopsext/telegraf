#Debug build
#docker run -it -v /Volumes/DATA/iprudnikov/DevNS/src:/go/src --rm --workdir "/go/src/github.com/influxdata/telegraf" \
#--env "GOPATH=/go" --env "GOOS=linux" --env "GOARCH=amd64" --env "CGO_ENABLED=0" \
#golang:latest go build -gcflags 'all=-N -l' -ldflags "-X main.version=1.9.3.7.debug.local -extldflags '-static'"  \
#-v -o ./plugins/inputs/docker_cnt_logs/deb/collector ./cmd/telegraf && \

#Non debug build
docker run -it -v /Volumes/DATA/ilya.prudnikov/DevNS/src:/go/src --rm --workdir "/go/src/github.com/influxdata/telegraf" \
  --env "GOPATH=/go" --env "GOOS=linux" --env "GOARCH=amd64" --env "CGO_ENABLED=0" \
  golang:latest go build -ldflags "-X main.version=1.9.3.X.release.local -extldflags '-static'" \
  -v -o ./build/docker_cnt_logsDeb ./cmd/telegraf

#docker build --pull -f ./Dockerfile.debian ./deb --tag registry.exness.io/service/collector/pod:1.9.3.X.RL-debug && \
#docker push registry.exness.io/service/collector/pod:1.9.3.X.RL-debug
#kubectl delete pod -n platform-services test-pod2
#kubectl apply -f /Volumes/DATA/iprudnikov/GoogleDrive/Documents/Работа/Exness/Platform/k8s/Pod/2cP.yml
#dlv debug --listen=:40000 --headless=true --api-version=2
#dlv --listen=:40000 --headless=true --api-version=2 exec "/usr/local/bin/collector" -- --config /etc/telegraf/collector.conf
