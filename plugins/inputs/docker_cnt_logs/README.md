# Docker container logs Input Plugin

The docker_cnt_logs plugin uses docker API to stream logs from container.
To be able to use it, docker socket should be provided for runtime,
and docker container logging driver should be set to `json-file` or `journald`.

To query API, the possible oldest version used - 1.21 (https://docs-stage.docker.com/engine/api/v1.21/), 
to support as much of variety of docker versions as possible. 
To stream logs the following API endpoint is used `GET /containers/(id or name)/logs`

API version vs Docker version compatibility matrix: https://docs.docker.com/develop/sdk/
(see `API version matrix` chapter)


### Configuration:

```
[[inputs.docker_cnt_logs]]  
  
  # Interval to gather data from docker sock.
  # the longer the interval the fewer request is made towards docker API (less CPU utilization on dockerd).
  # On the other hand, this increase the delay between producing logs and delivering it. Reasonable trade off
  # should be chosen
  interval = "2000ms"
  
  # Docker Endpoint
  #  To use unix, set endpoint = "unix:///var/run/docker.sock" (/var/run/docker.sock is default mount path)
  #  To use TCP, set endpoint = "tcp://[ip]:[port]"
  #  To use environment variables (ie, docker-machine), set endpoint = "ENV"
  endpoint = "unix:///var/run/docker.sock"

  # Set container id (long or short from), or container name
  # to stream logs from 	
  cont_id = "a7c165cb0930"

  # Set initial chunk size (length of []byte buffer to read from docker socket)
  # If not set, default value of 'defaultInitialChunkSize = 10000' will be used
  initial_chunk_size = "10000"	

  # Set max chunk size (length of []byte buffer to read from docker socket)
  # If not set, default value of 'defaultMaxChunkSize = 50000' will be used
  # buffer can grow in capacity adjusting to volume of data received from docker sock 
  max_chunk_size = "50000"	
```

### Metrics:
- stream
  - fields:
	- value (string)
  - tags: - accroging to input config (no default tags added by plugin itself)
