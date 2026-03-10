# Rename test source - resources with old naming convention
# Tests autosuggest's ability to match similar names

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

# Old naming: type_environment_purpose
resource "null_resource" "db_prod_main" {
  triggers = {
    name = "production-database"
  }
}

resource "null_resource" "db_prod_replica" {
  triggers = {
    name = "production-replica"
  }
}

resource "null_resource" "cache_prod_sessions" {
  triggers = {
    name = "session-cache"
  }
}

resource "null_resource" "server_prod_web" {
  triggers = {
    name = "web-server"
  }
}

resource "null_resource" "server_prod_api" {
  triggers = {
    name = "api-server"
  }
}
