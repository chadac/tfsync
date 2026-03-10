# Compute workspace target
# Resources split from monolith - compute only

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

# Same resources, same names - direct match for autosuggest
resource "null_resource" "instance_web" {
  triggers = {
    name = "web-server"
    type = "t3.medium"
  }
}

resource "null_resource" "instance_api" {
  triggers = {
    name = "api-server"
    type = "t3.large"
  }
}

resource "null_resource" "load_balancer" {
  triggers = {
    name = "main-lb"
    type = "application"
  }
}
