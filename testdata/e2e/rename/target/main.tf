# Rename test target - resources with new naming convention
# New naming: purpose_type (simpler, no environment prefix)

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

# New naming convention - autosuggest should find partial matches
resource "null_resource" "main_database" {
  triggers = {
    name = "production-database"
  }
}

resource "null_resource" "replica_database" {
  triggers = {
    name = "production-replica"
  }
}

resource "null_resource" "sessions_cache" {
  triggers = {
    name = "session-cache"
  }
}

resource "null_resource" "web_server" {
  triggers = {
    name = "web-server"
  }
}

resource "null_resource" "api_server" {
  triggers = {
    name = "api-server"
  }
}
