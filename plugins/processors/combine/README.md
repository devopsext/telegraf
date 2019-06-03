# Comnbine Processor Plugin

The `combine` plugin allows build new/replace existing tags and fields based on
 metric: name, timestamp, tags, fields. The output described in a form of go template.

### Configuration:

```toml
[[processors.combine]]
  format = "{{ index .tags \"source\"}}|{{index .tags \"process\"}}" 
  #Also available:
  # {{.name}} - metric name
  # {{.time}} - metric timestamp
  # {{index .fields "filedName"}} - access to metric fields
  # {{index .tags "tagName"}} - access to metric tags
  destType = "tag" # destination type - 'tag' or 'field'
  dest = "myNewTag" # name of the destination tag or field. If exist, will be overwritten
```

**Input**
```
stream,process=container,service=collector,source=docker_cnt_logs,stream=interactive,type=logs value="bla bla" 1557151832691546800
```

**Output**
```
stream,myNewTag=docker_cnt_logs|container,process=container,service=collector,source=docker_cnt_logs,stream=interactive,type=logs value="bla bla" 1557151832691546800
```
