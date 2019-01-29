# Stdout Processor Plugin

The stdout processor plugin prints every metric passing through it in predefined format.

### Configuration:

```toml
# Print metrics that pass through this filter using predefined format .
[[processors.stdout]]
  format = "{{.value}}"
```

### Tags:

No tags are applied by this processor.
