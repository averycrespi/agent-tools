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
  timeout               = "5m"
  max_pending           = 50
  max_pending_per_agent = 10
}

proxy_behavior {
  no_intercept_hosts = []
  max_body_buffer    = "1MiB"
  # allow_private_upstream controls whether the upstream dialer may connect to
  # RFC 1918 / loopback addresses. Cloud IMDS addresses (169.254.169.254,
  # fd00:ec2::254) are ALWAYS blocked regardless of this setting — they are an
  # unconditional SSRF exfil path and no legitimate upstream lives there.
  # Set to true only when your upstream services are on a private network.
  allow_private_upstream = false
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
}

log {
  level  = "info"
  format = "text"
}
