proxy {
  listen = "127.0.0.1:8220"
}

dashboard {
  listen       = "127.0.0.1:8221"
  open_browser = true
}

rules {
  dir = "~/.config/agent-gateway/rules.d"
}

secrets {
  cache_ttl = "60s"
}

audit {
  retention_days = 90
  prune_at       = "04:00"
}

approval {
  timeout     = "5m"
  max_pending = 50
}

proxy_behavior {
  no_intercept_hosts = []
  max_body_buffer    = "1MiB"
}

timeouts {
  connect_read_header      = "10s"
  mitm_handshake           = "10s"
  idle_keepalive           = "120s"
  upstream_dial            = "10s"
  upstream_tls             = "10s"
  upstream_response_header = "30s"
  upstream_idle_keepalive  = "90s"
  body_buffer_read         = "30s"
  request_body_read        = "0s"
  response_body_read       = "0s"
}

log {
  level  = "info"
  format = "text"
}
