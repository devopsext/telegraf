# Substring Processor Plugin

The substring processor plugin cuts every metric field value passing through it.

### Configuration:

```toml
[[processors.substring]]
  field = "value"
  position = 0
  max_length = 1024
```

### Tags:

No tags are applied by this processor.
