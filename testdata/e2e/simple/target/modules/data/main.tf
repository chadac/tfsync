# Data module - contains database and cache resources

resource "null_resource" "database" {
  triggers = {
    name = "production-db"
  }
}

resource "null_resource" "cache" {
  triggers = {
    name = "redis-cache"
  }
}
