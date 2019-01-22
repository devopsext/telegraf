docker run -it -v  /Volumes/DATA/iprudnikov/DevNS/telegraf/src:/go/src --rm --workdir "/go/src/github.com/influxdata/telegraf" --env "GOPATH=/go" golang:latest go build -ldflags "-X main.version=0.1 -s -w" -o ./plugins/inputs/docker_cnt_logs/deb/collector ./cmd/telegraf
docker build -f ./Dockerfile.debian ./deb --tag pod:stream
docker tag pod:stream registry.exness.io/service/collector/pod:stream
docker push registry.exness.io/service/collector/pod:stream
kubectl delete pod -n platform-services test-pod2
kubectl apply -f /Volumes/DATA/iprudnikov/GoogleDrive/Documents/Работа/Exness/Platform/k8s/Pod/2cP.yml