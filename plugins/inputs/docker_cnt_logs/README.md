# Docker container logs Input plugin

The docker_cnt_logs plugin uses docker API to stream logs from container.
To be able to use it, docker socket should be provided for runtime,
and docker container logging driver should be set to `json-file` or `journald`.

To query API, the possible oldest version used - 1.21 (https://docs-stage.docker.com/engine/api/v1.21/), 
to support as much of variety of docker versions as possible. 
To stream logs the following API endpoint is used `GET /containers/(id or name)/logs`

API version vs Docker version compatibility matrix: https://docs.docker.com/develop/sdk/
(see `API version matrix` chapter)

Primary features:
 - stream logs with a kind of back pressure, that realised through periodically pools the API. This allow 
 to handle cases when containers start streaming lots of output, without major CPU utilization.
 - offset is supported, that allows to stream logs from the particular point in time
even if telegraf crashed.
 - very well fitted and tested as a side-car container that stream logs from pod's containers in k8s and under rancher 1.6.x
 as a process inside container.

### Configuration:

```toml
[[inputs.docker_cnt_logs]]  
  ## Interval to gather data from docker sock.
  ## the longer the interval the fewer request is made towards docker API (less CPU utilization on dockerd).
  ## On the other hand, this increase the delay between producing logs and delivering it. Reasonable trade off
  ## should be chosen
  interval = "2000ms"
  
  ## Docker Endpoint
  ##  To use unix, set endpoint = "unix:///var/run/docker.sock" (/var/run/docker.sock is default mount path)
  ##  To use TCP, set endpoint = "tcp://[ip]:[port]"
  ##  To use environment variables (ie, docker-machine), set endpoint = "ENV"
  endpoint = "unix:///var/run/docker.sock"

  ## Optional TLS Config
  # tls_ca = "/etc/telegraf/ca.pem"
  # tls_cert = "/etc/telegraf/cert.pem"
  # tls_key = "/etc/telegraf/key.pem"

  ## Use TLS but skip chain & host verification
  # insecure_skip_verify = false

  ## Log streaming settings
  ## Set initial chunk size (length of []byte buffer to read from docker socket)
  ## If not set, default value of 'defaultInitialChunkSize = 1000' will be used
  # initial_chunk_size = 1000 # 1K symbols (half of 80x25 screen)

  ## Set max chunk size (length of []byte buffer to read from docker socket)
  ## If not set, default value of 'defaultMaxChunkSize = 5000' will be used
  ## buffer can grow in capacity adjusting to volume of data received from docker sock
  # max_chunk_size = 5000 # 5K symbols

  ## Offset flush interval. How often the offset pointer (see below) in the
  ## log stream is flashed to file.Offset pointer represents the unix time stamp
  ## for last message read from log stream (default - 3 sec)
  # offset_flush = "3s"

  ## Offset storage path (mandatory)
  offset_storage_path = "/var/run/collector_offset"

  ## Shutdown telegraf if all log streaming containers stopped/killed, default - false
  ## This option make sense when telegraf started especially for streaming logs
  ## in a form of sidecar container in k8s. In case primary container exited,
  ## side-car should be terminated also.
  # shutdown_when_eof = false

  ## Settings per container (specify as many sections as needed)
  [[inputs.docker_cnt_logs.container]]
    ## Set container id (long or short from), or container name
    ## to stream logs from, this attribute is mandatory
    id = "dc23d3ea534b3a6ec3934ae21e2dd4955fdbf61106b32fa19b831a6040a7feef"

    ## Override common settings
    ## input interval (specified or inherited from agent section)
    # interval = "500ms"

    ## Initial chunk size
    initial_chunk_size = 2000 # 2K symbols

    ## Max chunk size
    max_chunk_size = 6000 # 6K symbols

    #Set additional tags that will be tagged to the stream from the current container:
    tags = [
        "tag1=value1",
        "tag2=value2"
    ]
  ##Another container to stream logs from  
  [[inputs.docker_cnt_logs.container]]
    id = "009d82030745c9994e2f5c2280571e8b9f95681793a8f7073210759c74c1ea36"
    interval = "600ms" 	
```

### Metrics:
* stream
  - fields:
	- value (string), the message itself
  - tags:
    - conatainer_id
    - stream_type `stdin`,`stdout`,`interfactive`
