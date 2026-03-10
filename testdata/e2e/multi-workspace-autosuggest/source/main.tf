# Multi-workspace autosuggest source - monolith to be split
# Tests autosuggest's ability to match resources across workspace targets

terraform {
  backend "local" {
    path = "terraform.tfstate"
  }
}

# Networking resources (will go to networking workspace)
resource "null_resource" "vpc" {
  triggers = {
    name = "main-vpc"
    cidr = "10.0.0.0/16"
  }
}

resource "null_resource" "subnet_public" {
  triggers = {
    name = "public-subnet"
    cidr = "10.0.1.0/24"
  }
}

resource "null_resource" "security_group_web" {
  triggers = {
    name  = "web-sg"
    ports = "80,443"
  }
}

# Compute resources (will go to compute workspace)
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
