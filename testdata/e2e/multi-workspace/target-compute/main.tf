# Compute workspace - receives server resources

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

resource "null_resource" "web_server" {
  triggers = {
    name = "web-1"
    type = "t3.medium"
  }
}

resource "null_resource" "api_server" {
  triggers = {
    name = "api-1"
    type = "t3.large"
  }
}
