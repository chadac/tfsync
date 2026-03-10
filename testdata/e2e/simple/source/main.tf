# Simple source configuration for e2e testing
# Uses null_resource which doesn't require any cloud provider

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

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

resource "null_resource" "app_server" {
  triggers = {
    name = "web-server"
  }
}
