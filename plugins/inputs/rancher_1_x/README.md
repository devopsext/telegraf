# Rancher 1.x 'servicelog' API endpoint for completed events

....

### Configuration:

```
[[inputs.rancher_1_x]]  
  
  # Interval to gather data from API.
  # the longer the interval the fewer request is made towards rancher API.
  interval = "30s"
  
  # Rancher API Endpoint
  endpoint = "http://rancher.test.env"

  # Rancher API Acess key (https://github.com/rancher/api-spec/blob/master/specification.md#authentication)
  access_key = "*****"

  # Rancher API Secret key (https://github.com/rancher/api-spec/blob/master/specification.md#authentication)
  secret_key = "*****"

  # Rancher API timeout in seconds. Default value - 5
  api_timeout_sec = 5 

  # Service event types to be included into statistics.
  # 'like' syntax supported - "%.trigger%" 
  service_events_types_include = ["val1","val2"]
```

### Metrics:
TODO
- service_events_<event.type>
  - fields:
	- duration (float)
  - tags: - environment, stack, service, trahsactionId, description
